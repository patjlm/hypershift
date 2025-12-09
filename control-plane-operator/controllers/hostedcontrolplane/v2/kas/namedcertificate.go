package kas

import (
	"fmt"
	"strings"

	hyperv1 "github.com/openshift/hypershift/api/hypershift/v1beta1"
	component "github.com/openshift/hypershift/support/controlplane-component"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// adaptNamedCertificate adapts the cert-manager Certificate manifest for wildcard certificates.
// This creates a cert-manager Certificate that will generate a wildcard certificate for *.cluster.domain
// which can be used by kube-apiserver for serving HTTPS when configured via namedCertificates.
func adaptNamedCertificate(cpContext component.WorkloadContext, obj client.Object) error {
	hcp := cpContext.HCP
	cert := obj.(*unstructured.Unstructured)

	// Set namespace from HCP
	cert.SetNamespace(hcp.Namespace)

	// Extract wildcard domain from API hostname
	// API hostname format: api.{cluster-domain} → wildcard: *.{cluster-domain}
	// Note: Format and existence validated by enableNamedCertificate predicate
	apiHostname := getAPIServerHostname(hcp)
	clusterDomain := strings.TrimPrefix(apiHostname, "api.")
	wildcardDomain := fmt.Sprintf("*.%s", clusterDomain)

	// Update the dnsNames in the Certificate spec
	if err := unstructured.SetNestedStringSlice(cert.Object, []string{wildcardDomain}, "spec", "dnsNames"); err != nil {
		return fmt.Errorf("failed to set dnsNames: %w", err)
	}

	return nil
}

// getAPIServerHostname extracts the API server hostname from the HCP service publishing strategy
func getAPIServerHostname(hcp *hyperv1.HostedControlPlane) string {
	for _, svc := range hcp.Spec.Services {
		if svc.Service == hyperv1.APIServer {
			if svc.ServicePublishingStrategy.Route != nil {
				return svc.ServicePublishingStrategy.Route.Hostname
			}
		}
	}
	return ""
}

// enableNamedCertificate returns true if a Let's Encrypt wildcard certificate should be created.
// This is enabled when:
//  1. Platform is GCP
//  2. API server hostname is configured with expected format (api.{cluster-domain})
//  3. HostedCluster has configured spec.configuration.apiServer.servingCerts.namedCertificates
//     with a reference to the "external-api-cert" secret
func enableNamedCertificate(cpContext component.WorkloadContext) bool {
	hcp := cpContext.HCP

	// Only for GCP platform
	if hcp.Spec.Platform.Type != hyperv1.GCPPlatform {
		return false
	}

	// Require API hostname with expected format
	apiHostname := getAPIServerHostname(hcp)
	if apiHostname == "" || !strings.HasPrefix(apiHostname, "api.") {
		return false
	}

	// Check if namedCertificates configured for external-api-cert
	for _, cert := range hcp.Spec.Configuration.GetNamedCertificates() {
		if cert.ServingCertificate.Name == "external-api-cert" {
			return true
		}
	}

	return false
}
