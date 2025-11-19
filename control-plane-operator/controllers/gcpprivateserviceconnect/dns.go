package gcpprivateserviceconnect

import (
	"context"
	"fmt"
	"os"
	"strings"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"

	"google.golang.org/api/dns/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

// newDNSClient initializes a Cloud DNS client using GOOGLE_APPLICATION_CREDENTIALS.
// The environment variable should point to a service account JSON file
// (typically /etc/gcp/service-account.json mounted from gcp-customer-credentials secret).
func newDNSClient(ctx context.Context) (*dns.Service, error) {
	credFile := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if credFile == "" {
		return nil, fmt.Errorf("GOOGLE_APPLICATION_CREDENTIALS environment variable not set")
	}

	return dns.NewService(ctx, option.WithCredentialsFile(credFile))
}

// CreatePrivateZone creates a Cloud DNS private zone attached to a VPC network.
// If the zone already exists, it returns the existing zone.
//
// Parameters:
//   - projectID: GCP project ID where the zone will be created
//   - zoneName: DNS zone resource name (must match GCP naming: lowercase, hyphens, max 63 chars)
//   - dnsName: DNS domain name for the zone (must end with a dot, e.g., "my-cluster.hypershift.local.")
//   - vpcNetworkURL: Full GCP VPC network URL
//     Format: "https://www.googleapis.com/compute/v1/projects/{project}/global/networks/{network}"
func CreatePrivateZone(ctx context.Context, projectID, zoneName, dnsName, vpcNetworkURL string) (*dns.ManagedZone, error) {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure dnsName ends with a dot
	if !strings.HasSuffix(dnsName, ".") {
		dnsName += "."
	}

	zone := &dns.ManagedZone{
		Name:        zoneName,
		DnsName:     dnsName,
		Description: fmt.Sprintf("Private DNS zone for %s", dnsName),
		Visibility:  "private",
		PrivateVisibilityConfig: &dns.ManagedZonePrivateVisibilityConfig{
			Networks: []*dns.ManagedZonePrivateVisibilityConfigNetwork{
				{
					NetworkUrl: vpcNetworkURL,
				},
			},
		},
	}

	created, err := svc.ManagedZones.Create(projectID, zone).Context(ctx).Do()
	if err != nil {
		// Check if zone already exists
		if isAlreadyExists(err) {
			return svc.ManagedZones.Get(projectID, zoneName).Context(ctx).Do()
		}
		return nil, fmt.Errorf("failed to create private DNS zone %s in project %s: %w", zoneName, projectID, err)
	}

	return created, nil
}

// CreatePublicZone creates a Cloud DNS public zone.
// If the zone already exists, it returns the existing zone.
//
// Parameters:
//   - projectID: GCP project ID where the zone will be created
//   - zoneName: DNS zone resource name (must match GCP naming: lowercase, hyphens, max 63 chars)
//   - dnsName: DNS domain name for the zone (must end with a dot, e.g., "in.my-cluster.example.com.")
func CreatePublicZone(ctx context.Context, projectID, zoneName, dnsName string) (*dns.ManagedZone, error) {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return nil, err
	}

	// Ensure dnsName ends with a dot
	if !strings.HasSuffix(dnsName, ".") {
		dnsName += "."
	}

	zone := &dns.ManagedZone{
		Name:        zoneName,
		DnsName:     dnsName,
		Description: fmt.Sprintf("Public DNS zone for %s", dnsName),
		Visibility:  "public",
	}

	created, err := svc.ManagedZones.Create(projectID, zone).Context(ctx).Do()
	if err != nil {
		// Check if zone already exists
		if isAlreadyExists(err) {
			return svc.ManagedZones.Get(projectID, zoneName).Context(ctx).Do()
		}
		return nil, fmt.Errorf("failed to create public DNS zone %s in project %s: %w", zoneName, projectID, err)
	}

	return created, nil
}

// GetZone retrieves a Cloud DNS zone by name.
// Returns nil if the zone does not exist (without returning an error).
//
// Parameters:
//   - projectID: GCP project ID
//   - zoneName: DNS zone resource name
func GetZone(ctx context.Context, projectID, zoneName string) (*dns.ManagedZone, error) {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return nil, err
	}

	zone, err := svc.ManagedZones.Get(projectID, zoneName).Context(ctx).Do()
	if err != nil {
		// Return nil if zone doesn't exist
		if isNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get DNS zone %s in project %s: %w", zoneName, projectID, err)
	}

	return zone, nil
}

// DeleteZone deletes a Cloud DNS zone.
// Returns nil if the zone doesn't exist (idempotent).
//
// Parameters:
//   - projectID: GCP project ID
//   - zoneName: DNS zone resource name to delete
func DeleteZone(ctx context.Context, projectID, zoneName string) error {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return err
	}

	err = svc.ManagedZones.Delete(projectID, zoneName).Context(ctx).Do()
	if err != nil {
		// Ignore error if zone doesn't exist
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("failed to delete DNS zone %s in project %s: %w", zoneName, projectID, err)
	}

	return nil
}

// CreateCNAMERecord creates a CNAME record in a Cloud DNS zone.
// This operation is NOT idempotent - creating the same record twice will fail.
//
// Parameters:
//   - projectID: GCP project ID
//   - zoneName: DNS zone resource name where the record will be created
//   - recordName: Fully qualified DNS name for the record (e.g., "_acme-challenge.apps.example.com")
//   - target: CNAME target (e.g., "_acme-challenge.parent.example.com")
//   - ttl: Time to live in seconds (e.g., 300)
func CreateCNAMERecord(ctx context.Context, projectID, zoneName, recordName, target string, ttl int64) error {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return err
	}

	// Ensure names end with a dot (required by Cloud DNS)
	if !strings.HasSuffix(recordName, ".") {
		recordName += "."
	}
	if !strings.HasSuffix(target, ".") {
		target += "."
	}

	change := &dns.Change{
		Additions: []*dns.ResourceRecordSet{
			{
				Name:    recordName,
				Type:    "CNAME",
				Ttl:     ttl,
				Rrdatas: []string{target},
			},
		},
	}

	_, err = svc.Changes.Create(projectID, zoneName, change).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to create CNAME record %s -> %s in zone %s: %w", recordName, target, zoneName, err)
	}

	return nil
}

// CreateARecord creates an A record in a Cloud DNS zone.
// This operation is NOT idempotent - creating the same record twice will fail.
//
// Parameters:
//   - projectID: GCP project ID
//   - zoneName: DNS zone resource name where the record will be created
//   - recordName: Fully qualified DNS name for the record (e.g., "api.my-cluster.hypershift.local")
//   - ipAddress: IPv4 address (e.g., "10.0.0.100")
//   - ttl: Time to live in seconds (e.g., 300)
func CreateARecord(ctx context.Context, projectID, zoneName, recordName, ipAddress string, ttl int64) error {
	svc, err := newDNSClient(ctx)
	if err != nil {
		return err
	}

	// Ensure recordName ends with a dot (required by Cloud DNS)
	if !strings.HasSuffix(recordName, ".") {
		recordName += "."
	}

	change := &dns.Change{
		Additions: []*dns.ResourceRecordSet{
			{
				Name:    recordName,
				Type:    "A",
				Ttl:     ttl,
				Rrdatas: []string{ipAddress},
			},
		},
	}

	_, err = svc.Changes.Create(projectID, zoneName, change).Context(ctx).Do()
	if err != nil {
		return fmt.Errorf("failed to create A record %s -> %s in zone %s: %w", recordName, ipAddress, zoneName, err)
	}

	return nil
}

// GenerateZoneNames generates Cloud DNS zone names and DNS names from cluster name and base domain.
//
// Parameters:
//   - clusterName: Cluster name (e.g., "my-cluster")
//   - baseDomain: Cluster DNS zone base domain (e.g., "my-cluster-a1b2.us-central1-xyz789-1.dev.gcp-hcp.openshiftapps.com")
//
// Returns:
//   - hypershiftLocalZoneName: Zone name for hypershift.local (e.g., "my-cluster-hypershift-local")
//   - publicIngressZoneName: Zone name for public ingress (e.g., "in-my-cluster-a1b2-us-central1-xyz789-1-dev-gcp-hcp-openshiftapps-com-public")
//   - privateIngressZoneName: Zone name for private ingress (e.g., "in-my-cluster-a1b2-us-central1-xyz789-1-dev-gcp-hcp-openshiftapps-com-private")
//   - ingressDNSName: DNS name for ingress zones (e.g., "in.my-cluster-a1b2.us-central1-xyz789-1.dev.gcp-hcp.openshiftapps.com.")
//
// Zone names must comply with GCP naming requirements:
//   - Lowercase letters, numbers, and hyphens only
//   - Maximum 63 characters
//   - Start with a letter, end with a letter or number
func GenerateZoneNames(clusterName, baseDomain string) (hypershiftLocalZoneName, publicIngressZoneName, privateIngressZoneName, ingressDNSName string) {
	// hypershift.local zone name: {cluster}-hypershift-local
	hypershiftLocalZoneName = fmt.Sprintf("%s-hypershift-local", clusterName)

	// Convert base domain to zone name format (dots -> hyphens)
	// Example: "my-cluster-a1b2.us-central1-xyz789-1.dev.gcp-hcp.openshiftapps.com"
	//       -> "my-cluster-a1b2-us-central1-xyz789-1-dev-gcp-hcp-openshiftapps-com"
	baseZoneName := strings.ReplaceAll(baseDomain, ".", "-")

	// Ingress zone names: in-{base-zone-name}-{public|private}
	ingressZoneNameBase := "in-" + baseZoneName
	publicIngressZoneName = ingressZoneNameBase + "-public"
	privateIngressZoneName = ingressZoneNameBase + "-private"

	// Ingress DNS name: in.{baseDomain}. (note trailing dot)
	ingressDNSName = "in." + baseDomain + "."

	return
}

// isAlreadyExists checks if a GCP API error indicates a resource already exists.
func isAlreadyExists(err error) bool {
	if apiErr, ok := err.(*googleapi.Error); ok {
		return apiErr.Code == 409 // HTTP 409 Conflict
	}
	return false
}

// isNotFound checks if a GCP API error indicates a resource was not found.
func isNotFound(err error) bool {
	if apiErr, ok := err.(*googleapi.Error); ok {
		return apiErr.Code == 404 // HTTP 404 Not Found
	}
	return false
}

// DNSSetupResult contains the results of setting up cluster DNS zones.
type DNSSetupResult struct {
	// HypershiftLocalZone is the hypershift.local private zone
	HypershiftLocalZone *dns.ManagedZone

	// PublicIngressZone is the public ingress zone
	PublicIngressZone *dns.ManagedZone

	// PrivateIngressZone is the private ingress zone
	PrivateIngressZone *dns.ManagedZone

	// HypershiftLocalZoneName is the zone name for hypershift.local
	HypershiftLocalZoneName string

	// PublicIngressZoneName is the zone name for public ingress
	PublicIngressZoneName string

	// PrivateIngressZoneName is the zone name for private ingress
	PrivateIngressZoneName string

	// IngressDNSName is the DNS name for ingress zones (e.g., "in.{baseDomain}.")
	IngressDNSName string

	// PublicIngressNSRecords are the authoritative name servers for the public ingress zone.
	// These must be delegated from the regional zone by the CLS/CLM delegation controller.
	// Example: ["ns-cloud-a1.googledomains.com.", "ns-cloud-a2.googledomains.com.", ...]
	// Note: Private zones don't need delegation - they're only accessible within the VPC.
	PublicIngressNSRecords []string
}

// SetupClusterDNS creates all required DNS zones and records for a GCP HCP cluster.
// This is the main entry point for DNS zone creation from the PSC controller.
//
// Creates:
//  1. Private hypershift.local zone for internal cluster communication
//  2. Public ingress zone for external access (with ACME challenge delegation)
//  3. Private ingress zone for VPC-internal access
//  4. A record for api.{cluster}.hypershift.local -> PSC endpoint IP
//
// Parameters:
//   - ctx: Context for the operation
//   - hcp: HostedControlPlane CR containing cluster configuration
//   - pscEndpointIP: IP address of the Private Service Connect endpoint
//
// Returns:
//   - DNSSetupResult: Contains created zones and their names for status updates
//   - error: Any error encountered during setup
func SetupClusterDNS(ctx context.Context, hcp *hyperv1.HostedControlPlane, pscEndpointIP string) (*DNSSetupResult, error) {
	// Validate input
	if hcp.Spec.Platform.GCP == nil {
		return nil, fmt.Errorf("GCP platform spec is nil")
	}

	gcpSpec := hcp.Spec.Platform.GCP

	// Check if DNS creation is enabled
	if gcpSpec.CreateDnsZones == nil || !*gcpSpec.CreateDnsZones {
		return nil, fmt.Errorf("DNS zone creation is not enabled (spec.platform.gcp.createDnsZones)")
	}

	// Validate required fields
	if gcpSpec.ClusterDNSZoneBaseDomain == "" {
		return nil, fmt.Errorf("clusterDNSZoneBaseDomain is required when createDnsZones is true")
	}
	if gcpSpec.Project == "" {
		return nil, fmt.Errorf("GCP project is required")
	}
	if gcpSpec.NetworkConfig.Network.Name == "" {
		return nil, fmt.Errorf("VPC network name is required")
	}
	if pscEndpointIP == "" {
		return nil, fmt.Errorf("PSC endpoint IP is required")
	}

	// Extract configuration
	clusterName := hcp.Name
	baseDomain := gcpSpec.ClusterDNSZoneBaseDomain
	projectID := gcpSpec.Project
	vpcNetwork := gcpSpec.NetworkConfig.Network.Name

	// Generate zone names
	hypershiftZone, publicZone, privateZone, ingressDNS := GenerateZoneNames(clusterName, baseDomain)

	// Construct VPC network URL
	vpcNetworkURL := fmt.Sprintf(
		"https://www.googleapis.com/compute/v1/projects/%s/global/networks/%s",
		projectID, vpcNetwork)

	result := &DNSSetupResult{
		HypershiftLocalZoneName: hypershiftZone,
		PublicIngressZoneName:   publicZone,
		PrivateIngressZoneName:  privateZone,
		IngressDNSName:          ingressDNS,
	}

	// 1. Create hypershift.local private zone
	hypershiftDNSName := fmt.Sprintf("%s.hypershift.local.", clusterName)
	zone, err := CreatePrivateZone(ctx, projectID, hypershiftZone, hypershiftDNSName, vpcNetworkURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create hypershift.local zone: %w", err)
	}
	result.HypershiftLocalZone = zone

	// 2. Create public ingress zone
	zone, err = CreatePublicZone(ctx, projectID, publicZone, ingressDNS)
	if err != nil {
		return nil, fmt.Errorf("failed to create public ingress zone: %w", err)
	}
	result.PublicIngressZone = zone
	result.PublicIngressNSRecords = zone.NameServers

	// 3. Create private ingress zone
	zone, err = CreatePrivateZone(ctx, projectID, privateZone, ingressDNS, vpcNetworkURL)
	if err != nil {
		return nil, fmt.Errorf("failed to create private ingress zone: %w", err)
	}
	result.PrivateIngressZone = zone
	// Note: No NS records needed - private zones are VPC-scoped, not delegated

	// 4. Create ACME challenge CNAME record in public zone
	// This delegates ACME challenges to the regional zone
	acmeRecordName := fmt.Sprintf("_acme-challenge.apps.%s", ingressDNS)
	acmeTarget := fmt.Sprintf("_acme-challenge.%s.", baseDomain)
	if err := CreateCNAMERecord(ctx, projectID, publicZone, acmeRecordName, acmeTarget, 300); err != nil {
		// Don't fail if record already exists
		if !isAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create ACME challenge CNAME: %w", err)
		}
	}

	// 5. Create api A record in hypershift.local zone pointing to PSC endpoint
	apiRecordName := fmt.Sprintf("api.%s", hypershiftDNSName)
	if err := CreateARecord(ctx, projectID, hypershiftZone, apiRecordName, pscEndpointIP, 60); err != nil {
		// Don't fail if record already exists
		if !isAlreadyExists(err) {
			return nil, fmt.Errorf("failed to create api A record: %w", err)
		}
	}

	return result, nil
}

// CleanupClusterDNS deletes all DNS zones created for a cluster.
// This should be called when deleting a cluster.
//
// Parameters:
//   - ctx: Context for the operation
//   - hcp: HostedControlPlane CR containing cluster configuration
//
// Returns:
//   - error: Any error encountered during cleanup (idempotent - ignores not found errors)
func CleanupClusterDNS(ctx context.Context, hcp *hyperv1.HostedControlPlane) error {
	if hcp.Spec.Platform.GCP == nil {
		return nil // Nothing to clean up
	}

	gcpSpec := hcp.Spec.Platform.GCP

	// Skip if DNS creation was never enabled
	if gcpSpec.CreateDnsZones == nil || !*gcpSpec.CreateDnsZones {
		return nil
	}

	if gcpSpec.ClusterDNSZoneBaseDomain == "" || gcpSpec.Project == "" {
		return nil // Can't determine what to delete
	}

	clusterName := hcp.Name
	baseDomain := gcpSpec.ClusterDNSZoneBaseDomain
	projectID := gcpSpec.Project

	// Generate zone names
	hypershiftZone, publicZone, privateZone, _ := GenerateZoneNames(clusterName, baseDomain)

	// Delete all zones (errors are ignored if zones don't exist)
	var errs []error

	if err := DeleteZone(ctx, projectID, hypershiftZone); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete hypershift.local zone: %w", err))
	}

	if err := DeleteZone(ctx, projectID, publicZone); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete public ingress zone: %w", err))
	}

	if err := DeleteZone(ctx, projectID, privateZone); err != nil {
		errs = append(errs, fmt.Errorf("failed to delete private ingress zone: %w", err))
	}

	if len(errs) > 0 {
		return fmt.Errorf("DNS cleanup errors: %v", errs)
	}

	return nil
}
