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
	deletePublicZoneFlag = "delete-public-zone"
)

type DestroyInfraOptions struct {
	ProjectID        string
	InfraID          string
	Name             string
	Region           string
	BaseDomain       string
	DeletePublicZone bool
	InputFile        string
}

func NewDestroyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:          "gcp",
		Short:        "Destroys GCP infrastructure resources for a cluster",
		SilenceUsage: true,
	}

	opts := bindDestroyOptions(cmd)

	logger := log.Log
	cmd.PreRunE = func(cmd *cobra.Command, args []string) error {
		return opts.ValidateDestroyInputs()
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if err := opts.RunDestroy(cmd.Context(), logger); err != nil {
			logger.Error(err, "Failed to destroy GCP infrastructure")
			return err
		}
		return nil
	}

	return cmd
}

func bindDestroyOptions(cmd *cobra.Command) *DestroyInfraOptions {
	o := &DestroyInfraOptions{}
	cmd.Flags().StringVar(&o.InfraID, infraIDFlag, o.InfraID, "Infrastructure ID used for GCP resources")
	cmd.Flags().StringVar(&o.ProjectID, projectIDFlag, o.ProjectID, "GCP Project ID where resources were created")
	cmd.Flags().StringVar(&o.Name, clusterNameFlag, o.Name, "Cluster name used during creation")
	cmd.Flags().StringVar(&o.Region, regionFlag, o.Region, "GCP region where resources were created")
	cmd.Flags().StringVar(&o.BaseDomain, baseDomainFlag, o.BaseDomain, "Base domain used during creation")
	cmd.Flags().BoolVar(&o.DeletePublicZone, deletePublicZoneFlag, false, "If true, delete the public DNS zone for base domain (default: keep zone)")
	cmd.Flags().StringVar(&o.InputFile, "input-file", o.InputFile, "Path to input JSON file from create command (optional)")

	return o
}

func (o *DestroyInfraOptions) ValidateDestroyInputs() error {
	// If input file is provided, try to load from it
	if o.InputFile != "" {
		data, err := os.ReadFile(o.InputFile)
		if err != nil {
			return fmt.Errorf("failed to read input file: %w", err)
		}

		var output CreateInfraOutput
		if err := json.Unmarshal(data, &output); err != nil {
			return fmt.Errorf("failed to parse input file: %w", err)
		}

		// Override with values from file if not already set via flags
		if o.ProjectID == "" {
			o.ProjectID = output.ProjectID
		}
		if o.InfraID == "" {
			o.InfraID = output.InfraID
		}
		if o.Name == "" {
			o.Name = output.Name
		}
		if o.Region == "" {
			o.Region = output.Region
		}
		if o.BaseDomain == "" {
			o.BaseDomain = output.BaseDomain
		}
	}

	// Validate required fields
	if o.ProjectID == "" {
		return fmt.Errorf("project-id is required")
	}
	if o.Name == "" {
		return fmt.Errorf("cluster-name is required")
	}
	if o.Region == "" {
		return fmt.Errorf("region is required")
	}

	return nil
}

func (o *DestroyInfraOptions) RunDestroy(ctx context.Context, logger logr.Logger) error {
	return o.DestroyInfra(ctx, logger)
}

func (o *DestroyInfraOptions) DestroyInfra(ctx context.Context, logger logr.Logger) error {
	logger.Info("Destroying GCP infrastructure",
		"infraID", o.InfraID,
		"projectID", o.ProjectID,
		"name", o.Name,
		"region", o.Region,
		"baseDomain", o.BaseDomain)

	// Initialize managers
	networkMgr, err := NewNetworkManager(ctx, o.ProjectID, o.InfraID, o.Region, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize network manager: %w", err)
	}

	dnsMgr, err := NewDNSManager(ctx, o.ProjectID, o.InfraID, logger)
	if err != nil {
		return fmt.Errorf("failed to initialize DNS manager: %w", err)
	}

	var errs []error

	// Delete DNS zones first (in reverse order of creation)
	// 1. Delete local zone: {cluster}.hypershift.local
	logger.Info("Deleting local DNS zone")
	if err := dnsMgr.DeleteLocalZone(ctx, o.Name); err != nil {
		logger.Error(err, "Failed to delete local DNS zone")
		errs = append(errs, fmt.Errorf("failed to delete local DNS zone: %w", err))
	}

	// 2. Delete private zone: {cluster}.{basedomain}
	if o.BaseDomain != "" {
		logger.Info("Deleting private DNS zone")
		if err := dnsMgr.DeletePrivateZone(ctx, o.Name, o.BaseDomain); err != nil {
			logger.Error(err, "Failed to delete private DNS zone")
			errs = append(errs, fmt.Errorf("failed to delete private DNS zone: %w", err))
		}
	}

	// 3. Cleanup public zone records (ALWAYS)
	if o.BaseDomain != "" {
		logger.Info("Cleaning up public DNS zone records")
		if err := dnsMgr.CleanupPublicZone(ctx, o.Name, o.BaseDomain); err != nil {
			logger.Error(err, "Failed to cleanup public DNS zone records")
			// Don't add to errs - record cleanup failure shouldn't fail the whole operation
		}

		// Optionally delete the public zone itself
		if o.DeletePublicZone {
			logger.Info("Deleting public DNS zone")
			if err := dnsMgr.DeletePublicZone(ctx, o.BaseDomain); err != nil {
				logger.Error(err, "Failed to delete public DNS zone")
				errs = append(errs, fmt.Errorf("failed to delete public DNS zone: %w", err))
			}
		} else {
			logger.Info("Keeping public DNS zone (use --delete-public-zone to delete)")
		}
	}

	// Delete network resources (in reverse order of creation)
	// 4. Delete Cloud NAT (removes NAT config from router)
	logger.Info("Deleting Cloud NAT")
	if err := networkMgr.DeleteCloudNAT(ctx, o.Name); err != nil {
		logger.Error(err, "Failed to delete Cloud NAT")
		errs = append(errs, fmt.Errorf("failed to delete Cloud NAT: %w", err))
	}

	// 5. Delete Cloud Router
	logger.Info("Deleting Cloud Router")
	if err := networkMgr.DeleteCloudRouter(ctx, o.Name); err != nil {
		logger.Error(err, "Failed to delete Cloud Router")
		errs = append(errs, fmt.Errorf("failed to delete Cloud Router: %w", err))
	}

	// 6. Delete Subnet
	logger.Info("Deleting subnet")
	if err := networkMgr.DeleteSubnet(ctx, o.Name); err != nil {
		logger.Error(err, "Failed to delete subnet")
		errs = append(errs, fmt.Errorf("failed to delete subnet: %w", err))
	}

	// 7. Delete VPC Network (must be last)
	logger.Info("Deleting VPC network")
	if err := networkMgr.DeleteVPC(ctx, o.Name); err != nil {
		logger.Error(err, "Failed to delete VPC network")
		errs = append(errs, fmt.Errorf("failed to delete VPC network: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d errors during destroy: %v", len(errs), errs)
	}

	logger.Info("Successfully destroyed GCP infrastructure", "infraID", o.InfraID, "name", o.Name)
	return nil
}
