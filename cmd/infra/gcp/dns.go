package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
)

const (
	// hypershiftLocalZoneName is the DNS zone suffix for internal cluster DNS
	hypershiftLocalZoneName = "hypershift.local"
)

// DNSManager handles Cloud DNS operations for HyperShift clusters
type DNSManager struct {
	projectID string
	infraID   string
	dnsService *dns.Service
	logger    logr.Logger
}

// NewDNSManager creates a new DNS manager instance
func NewDNSManager(ctx context.Context, projectID, infraID string, logger logr.Logger) (*DNSManager, error) {
	dnsService, err := dns.NewService(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS service client: %w", err)
	}

	return &DNSManager{
		projectID:  projectID,
		infraID:    infraID,
		dnsService: dnsService,
		logger:     logger,
	}, nil
}

// LookupPublicZone looks up an existing public DNS zone by domain name
func (m *DNSManager) LookupPublicZone(ctx context.Context, baseDomain string) (string, error) {
	m.logger.Info("Looking up public DNS zone", "baseDomain", baseDomain)

	// List all managed zones in the project
	resp, err := m.dnsService.ManagedZones.List(m.projectID).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to list DNS zones: %w", err)
	}

	// Find zone matching the base domain
	targetDNS := baseDomain
	if !strings.HasSuffix(targetDNS, ".") {
		targetDNS += "."
	}

	for _, zone := range resp.ManagedZones {
		if zone.DnsName == targetDNS && zone.Visibility == "public" {
			m.logger.Info("Found public DNS zone", "baseDomain", baseDomain, "zoneID", zone.Id, "zoneName", zone.Name)
			return fmt.Sprintf("%d", zone.Id), nil
		}
	}

	return "", fmt.Errorf("public DNS zone not found for domain: %s", baseDomain)
}

// CreatePublicZone creates a public DNS managed zone for the base domain
// This zone is used for ACME DNS challenges, certificate provisioning, and public ingress
func (m *DNSManager) CreatePublicZone(ctx context.Context, baseDomain string) (string, error) {
	// Sanitize the base domain to create a zone name
	zoneName := sanitizeZoneName(baseDomain)

	m.logger.Info("Creating public DNS zone", "baseDomain", baseDomain, "zoneName", zoneName)

	// Check if zone already exists
	existingZone, err := m.dnsService.ManagedZones.Get(m.projectID, zoneName).Context(ctx).Do()
	if err == nil {
		m.logger.Info("Found existing public DNS zone", "baseDomain", baseDomain, "zoneID", existingZone.Id)
		return fmt.Sprintf("%d", existingZone.Id), nil
	}

	// Only proceed if error was 404 (not found)
	if !isNotFoundError(err) {
		return "", fmt.Errorf("failed to check if DNS zone exists: %w", err)
	}

	// Ensure base domain ends with dot
	dnsName := baseDomain
	if !strings.HasSuffix(dnsName, ".") {
		dnsName += "."
	}

	// Create the public managed zone
	managedZone := &dns.ManagedZone{
		Name:        zoneName,
		DnsName:     dnsName,
		Description: fmt.Sprintf("HyperShift public DNS for base domain %s (infra: %s)", baseDomain, m.infraID),
		Visibility:  "public",
	}

	createdZone, err := m.dnsService.ManagedZones.Create(m.projectID, managedZone).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create public DNS zone: %w", err)
	}

	m.logger.Info("Created public DNS zone", "baseDomain", baseDomain, "zoneID", createdZone.Id, "zoneName", createdZone.Name)
	return fmt.Sprintf("%d", createdZone.Id), nil
}

// CreatePrivateZone creates a private DNS managed zone for the cluster base domain
// This is different from the hypershift.local zone - it's for cluster-specific DNS
func (m *DNSManager) CreatePrivateZone(ctx context.Context, clusterName, baseDomain, networkURL string) (string, error) {
	// Zone DNS name: {cluster}.{basedomain}
	zoneDNSName := fmt.Sprintf("%s.%s", clusterName, baseDomain)
	zoneName := sanitizeZoneName(zoneDNSName)

	m.logger.Info("Creating private zone", "zoneDNSName", zoneDNSName, "zoneName", zoneName, "network", networkURL)

	return m.createPrivateZoneInternal(ctx, zoneName, zoneDNSName, networkURL, fmt.Sprintf("HyperShift private DNS for cluster %s (infra: %s)", clusterName, m.infraID))
}

// CreateLocalZone creates the hypershift.local private DNS zone
// This zone is used for internal cluster DNS resolution via Private Service Connect
func (m *DNSManager) CreateLocalZone(ctx context.Context, clusterName, networkURL string) (string, error) {
	zoneDNSName := fmt.Sprintf("%s.%s", clusterName, hypershiftLocalZoneName)
	zoneName := sanitizeZoneName(zoneDNSName)

	m.logger.Info("Creating local zone", "zoneDNSName", zoneDNSName, "zoneName", zoneName, "network", networkURL)

	return m.createPrivateZoneInternal(ctx, zoneName, zoneDNSName, networkURL, fmt.Sprintf("HyperShift internal DNS for cluster %s (infra: %s)", clusterName, m.infraID))
}

// createPrivateZoneInternal is the internal implementation for creating private zones
func (m *DNSManager) createPrivateZoneInternal(ctx context.Context, zoneName, zoneDNSName, networkURL, description string) (string, error) {
	// Check if zone already exists
	existingZone, err := m.dnsService.ManagedZones.Get(m.projectID, zoneName).Context(ctx).Do()
	if err == nil {
		m.logger.Info("Found existing private DNS zone", "zoneDNSName", zoneDNSName, "zoneID", existingZone.Id)
		return fmt.Sprintf("%d", existingZone.Id), nil
	}

	// Only proceed if error was 404 (not found)
	if !isNotFoundError(err) {
		return "", fmt.Errorf("failed to check if DNS zone exists: %w", err)
	}

	// Create the managed zone
	managedZone := &dns.ManagedZone{
		Name:        zoneName,
		DnsName:     zoneDNSName + ".", // Must end with dot per DNS standard
		Description: description,
		Visibility:  "private",
		PrivateVisibilityConfig: &dns.ManagedZonePrivateVisibilityConfig{
			Networks: []*dns.ManagedZonePrivateVisibilityConfigNetwork{
				{
					NetworkUrl: networkURL,
				},
			},
		},
	}

	createdZone, err := m.dnsService.ManagedZones.Create(m.projectID, managedZone).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("failed to create DNS zone: %w", err)
	}

	m.logger.Info("Created private DNS zone", "zoneDNSName", zoneDNSName, "zoneID", createdZone.Id, "zoneName", createdZone.Name)
	return fmt.Sprintf("%d", createdZone.Id), nil
}

// sanitizeZoneName converts a DNS name to a GCP-safe managed zone name
// GCP zone names must start with a letter and contain only lowercase letters, digits, and hyphens
// Example: "my-cluster.hypershift.local" -> "my-cluster-hypershift-local"
func sanitizeZoneName(dnsName string) string {
	// Remove trailing dot if present
	dnsName = strings.TrimSuffix(dnsName, ".")

	// Replace dots with hyphens
	return strings.ReplaceAll(dnsName, ".", "-")
}

// isNotFoundError checks if an error is a 404 Not Found error from GCP API
func isNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	if apiErr, ok := err.(*googleapi.Error); ok {
		return apiErr.Code == 404
	}

	return false
}

// CleanupPublicZone removes DNS records created by the cluster from the public zone
// but does not delete the zone itself
func (m *DNSManager) CleanupPublicZone(ctx context.Context, clusterName, baseDomain string) error {
	m.logger.Info("Cleaning up public DNS zone records", "baseDomain", baseDomain)

	// Lookup the public zone to get its actual name
	zone, err := m.lookupPublicZoneObject(ctx, baseDomain)
	if err != nil {
		// Zone doesn't exist or lookup failed - skip cleanup gracefully
		m.logger.Info("Public zone not found, skipping record cleanup", "baseDomain", baseDomain)
		return nil
	}

	// Delete wildcard ingress record: *.apps.<cluster>.<basedomain>
	wildcardRecord := fmt.Sprintf("*.apps.%s.%s", clusterName, baseDomain)
	if err := m.deleteRecord(ctx, zone.Name, wildcardRecord); err != nil {
		// Log but don't fail - record might not exist or already deleted
		m.logger.Info("Skipping wildcard record deletion", "record", wildcardRecord, "reason", err.Error())
		return nil
	}

	m.logger.Info("Cleaned up public DNS zone records", "zoneID", zone.Id, "zoneName", zone.Name, "record", wildcardRecord)
	return nil
}

// lookupPublicZoneObject looks up a public DNS zone and returns the full zone object
func (m *DNSManager) lookupPublicZoneObject(ctx context.Context, baseDomain string) (*dns.ManagedZone, error) {
	// List all managed zones in the project
	resp, err := m.dnsService.ManagedZones.List(m.projectID).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("failed to list DNS zones: %w", err)
	}

	// Find zone matching the base domain
	targetDNS := baseDomain
	if !strings.HasSuffix(targetDNS, ".") {
		targetDNS += "."
	}

	for _, zone := range resp.ManagedZones {
		if zone.DnsName == targetDNS && zone.Visibility == "public" {
			return zone, nil
		}
	}

	return nil, fmt.Errorf("public DNS zone not found for domain: %s", baseDomain)
}

// DeletePublicZone deletes the public DNS managed zone for the base domain
func (m *DNSManager) DeletePublicZone(ctx context.Context, baseDomain string) error {
	zoneName := sanitizeZoneName(baseDomain)
	m.logger.Info("Deleting public DNS zone", "baseDomain", baseDomain, "zoneName", zoneName)

	return m.deleteZone(ctx, zoneName, baseDomain)
}

// DeletePrivateZone deletes the private DNS managed zone for the cluster
func (m *DNSManager) DeletePrivateZone(ctx context.Context, clusterName, baseDomain string) error {
	zoneDNSName := fmt.Sprintf("%s.%s", clusterName, baseDomain)
	zoneName := sanitizeZoneName(zoneDNSName)

	m.logger.Info("Deleting private DNS zone", "zoneDNSName", zoneDNSName, "zoneName", zoneName)

	return m.deleteZone(ctx, zoneName, zoneDNSName)
}

// DeleteLocalZone deletes the hypershift.local DNS managed zone
func (m *DNSManager) DeleteLocalZone(ctx context.Context, clusterName string) error {
	zoneDNSName := fmt.Sprintf("%s.%s", clusterName, hypershiftLocalZoneName)
	zoneName := sanitizeZoneName(zoneDNSName)

	m.logger.Info("Deleting local DNS zone", "zoneDNSName", zoneDNSName, "zoneName", zoneName)

	return m.deleteZone(ctx, zoneName, zoneDNSName)
}

// deleteZone is the internal implementation for deleting DNS zones
func (m *DNSManager) deleteZone(ctx context.Context, zoneName, zoneDNSName string) error {
	// Check if zone exists
	_, err := m.dnsService.ManagedZones.Get(m.projectID, zoneName).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("DNS zone not found, skipping deletion", "zoneName", zoneName)
			return nil
		}
		return fmt.Errorf("failed to check if DNS zone exists: %w", err)
	}

	// Delete the managed zone
	if err := m.dnsService.ManagedZones.Delete(m.projectID, zoneName).Context(ctx).Do(); err != nil {
		if isNotFoundError(err) {
			m.logger.Info("DNS zone already deleted", "zoneName", zoneName)
			return nil
		}
		return fmt.Errorf("failed to delete DNS zone: %w", err)
	}

	m.logger.Info("Deleted DNS zone", "zoneDNSName", zoneDNSName, "zoneName", zoneName)
	return nil
}

// deleteRecord deletes a specific DNS record from a zone
func (m *DNSManager) deleteRecord(ctx context.Context, zoneName, recordName string) error {
	// Ensure record name ends with dot
	if !strings.HasSuffix(recordName, ".") {
		recordName += "."
	}

	// List all record sets in the zone to find the one to delete
	resp, err := m.dnsService.ResourceRecordSets.List(m.projectID, zoneName).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to list DNS records: %w", err)
	}

	// Find the record to delete
	var recordToDelete *dns.ResourceRecordSet
	for _, record := range resp.Rrsets {
		if record.Name == recordName {
			recordToDelete = record
			break
		}
	}

	if recordToDelete == nil {
		m.logger.Info("DNS record not found, skipping deletion", "record", recordName)
		return nil
	}

	// Create a change to delete the record
	change := &dns.Change{
		Deletions: []*dns.ResourceRecordSet{recordToDelete},
	}

	_, err = m.dnsService.Changes.Create(m.projectID, zoneName, change).Context(ctx).Do()
	if err != nil {
		if isNotFoundError(err) {
			m.logger.Info("DNS record already deleted", "record", recordName)
			return nil
		}
		return fmt.Errorf("failed to delete DNS record: %w", err)
	}

	m.logger.Info("Deleted DNS record", "record", recordName, "zone", zoneName)
	return nil
}
