package gcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"google.golang.org/api/compute/v1"
)

const (
	// DefaultVPCCIDR is the default CIDR block for the VPC network
	DefaultVPCCIDR = "10.0.0.0/16"

	// DefaultSubnetCIDR is the default CIDR block for the subnet
	DefaultSubnetCIDR = "10.0.0.0/20"
)

// NetworkManager handles GCP VPC and networking operations for HyperShift clusters
type NetworkManager struct {
	projectID      string
	infraID        string
	region         string
	computeService *compute.Service
	logger         logr.Logger
}

// NewNetworkManager creates a new network manager instance
func NewNetworkManager(ctx context.Context, projectID, infraID, region string, logger logr.Logger) (*NetworkManager, error) {
	computeService, err := compute.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create compute service client: %w", err)
	}

	return &NetworkManager{
		projectID:      projectID,
		infraID:        infraID,
		region:         region,
		computeService: computeService,
		logger:         logger,
	}, nil
}

// CreateVPC creates a VPC network with auto-mode subnets disabled
func (m *NetworkManager) CreateVPC(ctx context.Context, name string) (string, error) {
	networkName := fmt.Sprintf("%s-network", name)
	m.logger.Info("Creating VPC network", "name", networkName)

	// Check if network already exists
	existingNetwork, err := m.computeService.Networks.Get(m.projectID, networkName).Context(ctx).Do()
	if err == nil {
		m.logger.Info("Found existing VPC network", "name", networkName, "selfLink", existingNetwork.SelfLink)
		return existingNetwork.SelfLink, nil
	}

	// Only proceed if error was 404 (not found)
	if !isNotFoundError(err) {
		return "", fmt.Errorf("failed to check if VPC network exists: %w", err)
	}

	// Create the network
	network := &compute.Network{
		Name:                  networkName,
		Description:           fmt.Sprintf("HyperShift VPC network for %s (infra: %s)", name, m.infraID),
		AutoCreateSubnetworks: false, // We'll create custom subnets
		RoutingConfig: &compute.NetworkRoutingConfig{
			RoutingMode: "REGIONAL", // Regional routing for better isolation
		},
		// CRITICAL: Force the AutoCreateSubnetworks field to be sent to GCP
		// Without this, the false value is omitted and GCP defaults to deprecated legacy mode
		ForceSendFields: []string{"AutoCreateSubnetworks"},
	}

	op, err := m.computeService.Networks.Insert(m.projectID, network).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create VPC network: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForGlobalOperation(ctx, op.Name); err != nil {
		return "", fmt.Errorf("failed waiting for network creation: %w", err)
	}

	// Get the created network
	createdNetwork, err := m.computeService.Networks.Get(m.projectID, networkName).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to get created network: %w", err)
	}

	m.logger.Info("Created VPC network", "name", networkName, "selfLink", createdNetwork.SelfLink)
	return createdNetwork.SelfLink, nil
}

// CreateSubnet creates a subnet in the specified network
func (m *NetworkManager) CreateSubnet(ctx context.Context, name, networkURL, cidr string) (string, error) {
	subnetName := fmt.Sprintf("%s-subnet", name)
	m.logger.Info("Creating subnet", "name", subnetName, "cidr", cidr, "region", m.region)

	// Check if subnet already exists
	existingSubnet, err := m.computeService.Subnetworks.Get(m.projectID, m.region, subnetName).Context(ctx).Do()
	if err == nil {
		m.logger.Info("Found existing subnet", "name", subnetName, "selfLink", existingSubnet.SelfLink)
		return existingSubnet.SelfLink, nil
	}

	// Only proceed if error was 404 (not found)
	if !isNotFoundError(err) {
		return "", fmt.Errorf("failed to check if subnet exists: %w", err)
	}

	// Create the subnet
	subnet := &compute.Subnetwork{
		Name:        subnetName,
		Description: fmt.Sprintf("HyperShift subnet for %s (infra: %s)", name, m.infraID),
		Network:     networkURL,
		IpCidrRange: cidr,
		Region:      m.region,
		PrivateIpGoogleAccess: true, // Enable private Google access
	}

	op, err := m.computeService.Subnetworks.Insert(m.projectID, m.region, subnet).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create subnet: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return "", fmt.Errorf("failed waiting for subnet creation: %w", err)
	}

	// Get the created subnet
	createdSubnet, err := m.computeService.Subnetworks.Get(m.projectID, m.region, subnetName).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to get created subnet: %w", err)
	}

	m.logger.Info("Created subnet", "name", subnetName, "selfLink", createdSubnet.SelfLink)
	return createdSubnet.SelfLink, nil
}

// CreateCloudRouter creates a Cloud Router for NAT
func (m *NetworkManager) CreateCloudRouter(ctx context.Context, name, networkURL string) (string, error) {
	routerName := fmt.Sprintf("%s-router", name)
	m.logger.Info("Creating Cloud Router", "name", routerName, "region", m.region)

	// Check if router already exists
	existingRouter, err := m.computeService.Routers.Get(m.projectID, m.region, routerName).Context(ctx).Do()
	if err == nil {
		m.logger.Info("Found existing Cloud Router", "name", routerName, "selfLink", existingRouter.SelfLink)
		return existingRouter.Name, nil
	}

	// Only proceed if error was 404 (not found)
	if !isNotFoundError(err) {
		return "", fmt.Errorf("failed to check if router exists: %w", err)
	}

	// Create the router
	router := &compute.Router{
		Name:        routerName,
		Description: fmt.Sprintf("HyperShift Cloud Router for %s (infra: %s)", name, m.infraID),
		Network:     networkURL,
		Region:      m.region,
	}

	op, err := m.computeService.Routers.Insert(m.projectID, m.region, router).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create Cloud Router: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return "", fmt.Errorf("failed waiting for router creation: %w", err)
	}

	m.logger.Info("Created Cloud Router", "name", routerName)
	return routerName, nil
}

// CreateCloudNAT creates a Cloud NAT configuration on the router
func (m *NetworkManager) CreateCloudNAT(ctx context.Context, name, routerName string) error {
	natName := fmt.Sprintf("%s-nat", name)
	m.logger.Info("Creating Cloud NAT", "name", natName, "router", routerName)

	// Get the router to check if NAT already exists
	router, err := m.computeService.Routers.Get(m.projectID, m.region, routerName).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to get router: %w", err)
	}

	// Check if NAT configuration already exists
	for _, nat := range router.Nats {
		if nat.Name == natName {
			m.logger.Info("Found existing Cloud NAT", "name", natName)
			return nil
		}
	}

	// Add NAT configuration to router
	nat := &compute.RouterNat{
		Name: natName,
		NatIpAllocateOption: "AUTO_ONLY", // Automatically allocate IP addresses
		SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES",
		LogConfig: &compute.RouterNatLogConfig{
			Enable: false, // Disable logging for now
		},
	}

	router.Nats = append(router.Nats, nat)

	op, err := m.computeService.Routers.Patch(m.projectID, m.region, routerName, router).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to create Cloud NAT: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return fmt.Errorf("failed waiting for NAT creation: %w", err)
	}

	m.logger.Info("Created Cloud NAT", "name", natName)
	return nil
}

// waitForGlobalOperation waits for a global operation to complete
func (m *NetworkManager) waitForGlobalOperation(ctx context.Context, opName string) error {
	for {
		op, err := m.computeService.GlobalOperations.Get(m.projectID, opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to get operation status: %w", err)
		}

		if op.Status == "DONE" {
			if op.Error != nil {
				var errMsgs []string
				for _, e := range op.Error.Errors {
					errMsgs = append(errMsgs, e.Message)
				}
				return fmt.Errorf("operation failed: %s", strings.Join(errMsgs, "; "))
			}
			return nil
		}

		// Wait before checking again
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			// Continue polling
		}
	}
}

// waitForRegionalOperation waits for a regional operation to complete
func (m *NetworkManager) waitForRegionalOperation(ctx context.Context, opName string) error {
	for {
		op, err := m.computeService.RegionOperations.Get(m.projectID, m.region, opName).Context(ctx).Do()
		if err != nil {
			return fmt.Errorf("failed to get operation status: %w", err)
		}

		if op.Status == "DONE" {
			if op.Error != nil {
				var errMsgs []string
				for _, e := range op.Error.Errors {
					errMsgs = append(errMsgs, e.Message)
				}
				return fmt.Errorf("operation failed: %s", strings.Join(errMsgs, "; "))
			}
			return nil
		}

		// Wait before checking again
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			// Continue polling
		}
	}
}
// DeleteVPC deletes a VPC network
func (m *NetworkManager) DeleteVPC(ctx context.Context, name string) error {
	networkName := fmt.Sprintf("%s-network", name)
	m.logger.Info("Deleting VPC network", "name", networkName)

	// Check if network exists
	_, err := m.computeService.Networks.Get(m.projectID, networkName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("VPC network not found, skipping deletion", "name", networkName)
			return nil
		}
		return fmt.Errorf("failed to check if VPC network exists: %w", err)
	}

	// Delete the network
	op, err := m.computeService.Networks.Delete(m.projectID, networkName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("VPC network already deleted", "name", networkName)
			return nil
		}
		return fmt.Errorf("failed to delete VPC network: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForGlobalOperation(ctx, op.Name); err != nil {
		return fmt.Errorf("failed waiting for network deletion: %w", err)
	}

	m.logger.Info("Deleted VPC network", "name", networkName)
	return nil
}

// DeleteSubnet deletes a subnet
func (m *NetworkManager) DeleteSubnet(ctx context.Context, name string) error {
	subnetName := fmt.Sprintf("%s-subnet", name)
	m.logger.Info("Deleting subnet", "name", subnetName, "region", m.region)

	// Check if subnet exists
	_, err := m.computeService.Subnetworks.Get(m.projectID, m.region, subnetName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("Subnet not found, skipping deletion", "name", subnetName)
			return nil
		}
		return fmt.Errorf("failed to check if subnet exists: %w", err)
	}

	// Delete the subnet
	op, err := m.computeService.Subnetworks.Delete(m.projectID, m.region, subnetName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("Subnet already deleted", "name", subnetName)
			return nil
		}
		return fmt.Errorf("failed to delete subnet: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return fmt.Errorf("failed waiting for subnet deletion: %w", err)
	}

	m.logger.Info("Deleted subnet", "name", subnetName)
	return nil
}

// DeleteCloudRouter deletes a Cloud Router
func (m *NetworkManager) DeleteCloudRouter(ctx context.Context, name string) error {
	routerName := fmt.Sprintf("%s-router", name)
	m.logger.Info("Deleting Cloud Router", "name", routerName, "region", m.region)

	// Check if router exists
	_, err := m.computeService.Routers.Get(m.projectID, m.region, routerName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("Cloud Router not found, skipping deletion", "name", routerName)
			return nil
		}
		return fmt.Errorf("failed to check if router exists: %w", err)
	}

	// Delete the router
	op, err := m.computeService.Routers.Delete(m.projectID, m.region, routerName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("Cloud Router already deleted", "name", routerName)
			return nil
		}
		return fmt.Errorf("failed to delete Cloud Router: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return fmt.Errorf("failed waiting for router deletion: %w", err)
	}

	m.logger.Info("Deleted Cloud Router", "name", routerName)
	return nil
}

// DeleteCloudNAT removes the Cloud NAT configuration from the router
func (m *NetworkManager) DeleteCloudNAT(ctx context.Context, name string) error {
	natName := fmt.Sprintf("%s-nat", name)
	routerName := fmt.Sprintf("%s-router", name)
	m.logger.Info("Deleting Cloud NAT", "name", natName, "router", routerName)

	// Get the router
	router, err := m.computeService.Routers.Get(m.projectID, m.region, routerName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("Cloud Router not found, skipping NAT deletion", "router", routerName)
			return nil
		}
		return fmt.Errorf("failed to get router: %w", err)
	}

	// Check if NAT configuration exists
	var foundNAT bool
	var newNats []*compute.RouterNat
	for _, nat := range router.Nats {
		if nat.Name == natName {
			foundNAT = true
			// Skip this NAT (effectively removing it)
			continue
		}
		newNats = append(newNats, nat)
	}

	if !foundNAT {
		m.logger.Info("Cloud NAT not found, skipping deletion", "name", natName)
		return nil
	}

	// Update router with NAT removed
	router.Nats = newNats
	op, err := m.computeService.Routers.Patch(m.projectID, m.region, routerName, router).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to delete Cloud NAT: %w", err)
	}

	// Wait for operation to complete
	if err := m.waitForRegionalOperation(ctx, op.Name); err != nil {
		return fmt.Errorf("failed waiting for NAT deletion: %w", err)
	}

	m.logger.Info("Deleted Cloud NAT", "name", natName)
	return nil
}
