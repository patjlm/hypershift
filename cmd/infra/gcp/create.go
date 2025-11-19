package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"github.com/openshift/hypershift/cmd/log"
	"github.com/spf13/cobra"
)

const (
	clusterNameFlag      = "cluster-name"
	regionFlag           = "region"
	baseDomainFlag       = "base-domain"
	vpcCIDRFlag          = "vpc-cidr"
	createPublicZoneFlag = "create-public-zone"
)

type CreateInfraOptions struct {
	ProjectID        string
	InfraID          string
	Name             string
	Region           string
	BaseDomain       string
	VPCCIDR          string
	CreatePublicZone bool
	OutputFile       string
}

type CreateInfraOutput struct {
	ProjectID     string `json:"projectID"`
	InfraID       string `json:"infraID"`
	Region        string `json:"region"`
	Name          string `json:"name"`
	BaseDomain    string `json:"baseDomain"`
	MachineCIDR   string `json:"machineCIDR"`
	Network       string `json:"network"`       // VPC network self-link
	Subnetwork    string `json:"subnetwork"`    // Subnet self-link
	PublicZoneID  string `json:"publicZoneID"`  // Base domain public zone ID (looked up)
	PrivateZoneID string `json:"privateZoneID"` // {cluster}.{basedomain} private zone ID
	LocalZoneID   string `json:"localZoneID"`   // {cluster}.hypershift.local zone ID
}

func NewCreateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "gcp",
		Short:        "Creates GCP infrastructure resources for a cluster",
		SilenceUsage: true,
	}

	opts := bindCreateOptions(cmd)

	logger := log.Log
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		return opts.ValidateCreateInputs()
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := opts.RunCreate(cmd.Context(), logger); err != nil {
			logger.Error(err, "Failed to create GCP infrastructure")
			return err
		}
		return nil
	}

	return cmd
}

func bindCreateOptions(cmd *cobra.Command) *CreateInfraOptions {
	o := &CreateInfraOptions{}
	cmd.Flags().StringVar(&o.InfraID, infraIDFlag, o.InfraID, "Infrastructure ID to use for GCP resources")
	cmd.Flags().StringVar(&o.ProjectID, projectIDFlag, o.ProjectID, "GCP Project ID where resources will be created")
	cmd.Flags().StringVar(&o.Name, clusterNameFlag, o.Name, "Cluster name (used for resource naming and DNS)")
	cmd.Flags().StringVar(&o.Region, regionFlag, o.Region, "GCP region for resources (e.g., 'us-central1')")
	cmd.Flags().StringVar(&o.BaseDomain, baseDomainFlag, o.BaseDomain, "The ingress base domain for the cluster")
	cmd.Flags().StringVar(&o.VPCCIDR, vpcCIDRFlag, DefaultVPCCIDR, "CIDR block for the VPC network")
	cmd.Flags().BoolVar(&o.CreatePublicZone, createPublicZoneFlag, false, "If true, create the public DNS zone for base domain (default: lookup existing zone)")
	cmd.Flags().StringVar(&o.OutputFile, outputFileFlag, o.OutputFile, "Path to output JSON file with infrastructure details")

	_ = cmd.MarkFlagRequired(infraIDFlag)
	_ = cmd.MarkFlagRequired(projectIDFlag)
	_ = cmd.MarkFlagRequired(clusterNameFlag)
	_ = cmd.MarkFlagRequired(regionFlag)
	_ = cmd.MarkFlagRequired(baseDomainFlag)
	return o
}

func (o *CreateInfraOptions) ValidateCreateInputs() error {
	if o.InfraID == "" {
		return fmt.Errorf("infra-id is required")
	}
	if o.ProjectID == "" {
		return fmt.Errorf("project-id is required")
	}
	if o.Name == "" {
		return fmt.Errorf("cluster-name is required")
	}
	if o.Region == "" {
		return fmt.Errorf("region is required")
	}
	if o.BaseDomain == "" {
		return fmt.Errorf("base-domain is required")
	}
	if o.VPCCIDR == "" {
		o.VPCCIDR = DefaultVPCCIDR
	}
	return nil
}

func (o *CreateInfraOptions) RunCreate(ctx context.Context, logger logr.Logger) error {
	results, err := o.CreateInfra(ctx, logger)
	if err != nil {
		return err
	}
	return o.OutputCreate(results, logger)
}

func (o *CreateInfraOptions) OutputCreate(results *CreateInfraOutput, logger logr.Logger) error {
	out := os.Stdout
	if len(o.OutputFile) > 0 {
		var err error
		out, err = os.Create(o.OutputFile)
		if err != nil {
			return fmt.Errorf("cannot create output file: %w", err)
		}
		defer func(out *os.File) {
			if err := out.Close(); err != nil {
				logger.Error(err, "Failed to close output file", "file", o.OutputFile)
			}
		}(out)
	}
	outputBytes, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize result: %w", err)
	}
	_, err = out.Write(outputBytes)
	if err != nil {
		return fmt.Errorf("failed to write result: %w", err)
	}
	return nil
}

func (o *CreateInfraOptions) CreateInfra(ctx context.Context, logger logr.Logger) (*CreateInfraOutput, error) {
	logger.Info("Creating GCP infrastructure",
		"infraID", o.InfraID,
		"projectID", o.ProjectID,
		"name", o.Name,
		"region", o.Region,
		"baseDomain", o.BaseDomain,
		"vpcCIDR", o.VPCCIDR)

	result := &CreateInfraOutput{
		ProjectID:   o.ProjectID,
		InfraID:     o.InfraID,
		Region:      o.Region,
		Name:        o.Name,
		BaseDomain:  o.BaseDomain,
		MachineCIDR: o.VPCCIDR,
	}

	// Initialize managers
	networkMgr, err := NewNetworkManager(ctx, o.ProjectID, o.InfraID, o.Region, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize network manager: %w", err)
	}

	dnsMgr, err := NewDNSManager(ctx, o.ProjectID, o.InfraID, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize DNS manager: %w", err)
	}

	// Create VPC network
	logger.Info("Creating VPC network")
	networkURL, err := networkMgr.CreateVPC(ctx, o.Name)
	if err != nil {
		return nil, fmt.Errorf("failed to create VPC network: %w", err)
	}
	result.Network = networkURL

	// Create subnet
	logger.Info("Creating subnet")
	subnetURL, err := networkMgr.CreateSubnet(ctx, o.Name, networkURL, DefaultSubnetCIDR)
	if err != nil {
		return nil, fmt.Errorf("failed to create subnet: %w", err)
	}
	result.Subnetwork = subnetURL

	// Create Cloud Router
	logger.Info("Creating Cloud Router")
	routerName, err := networkMgr.CreateCloudRouter(ctx, o.Name, networkURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create Cloud Router: %w", err)
	}

	// Create Cloud NAT
	logger.Info("Creating Cloud NAT")
	if err := networkMgr.CreateCloudNAT(ctx, o.Name, routerName); err != nil {
		return nil, fmt.Errorf("failed to create Cloud NAT: %w", err)
	}

	// Create or lookup public zone based on flag
	var publicZoneID string
	if o.CreatePublicZone {
		logger.Info("Creating public DNS zone")
		publicZoneID, err = dnsMgr.CreatePublicZone(ctx, o.BaseDomain)
		if err != nil {
			return nil, fmt.Errorf("failed to create public zone: %w", err)
		}
	} else {
		logger.Info("Looking up public DNS zone")
		publicZoneID, err = dnsMgr.LookupPublicZone(ctx, o.BaseDomain)
		if err != nil {
			return nil, fmt.Errorf("failed to lookup public zone: %w", err)
		}
	}
	result.PublicZoneID = publicZoneID

	// Create private DNS zone: {cluster}.{basedomain}
	logger.Info("Creating private DNS zone")
	privateZoneID, err := dnsMgr.CreatePrivateZone(ctx, o.Name, o.BaseDomain, networkURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create private DNS zone: %w", err)
	}
	result.PrivateZoneID = privateZoneID

	// Create local DNS zone: {cluster}.hypershift.local
	logger.Info("Creating local DNS zone")
	localZoneID, err := dnsMgr.CreateLocalZone(ctx, o.Name, networkURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create local DNS zone: %w", err)
	}
	result.LocalZoneID = localZoneID

	logger.Info("Successfully created GCP infrastructure",
		"infraID", o.InfraID,
		"network", result.Network,
		"subnetwork", result.Subnetwork,
		"publicZoneID", result.PublicZoneID,
		"privateZoneID", result.PrivateZoneID,
		"localZoneID", result.LocalZoneID)

	return result, nil
}
