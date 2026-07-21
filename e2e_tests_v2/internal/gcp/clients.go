package gcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	osconfig "cloud.google.com/go/osconfig/apiv1"
	"cloud.google.com/go/osconfig/apiv1/osconfigpb"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/config"
	"github.com/GoogleCloudPlatform/osconfig/e2e_tests_v2/internal/poll"
	compute "google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Clients owns shared, concurrency-safe GCP API clients for one test process.
type Clients struct {
	compute  *compute.Service
	osconfig *osconfig.Client
	zonal    *osconfig.OsConfigZonalClient
	poll     time.Duration
}

// NewClients uses Application Default Credentials. Test configuration never
// carries credential material.
func NewClients(ctx context.Context, cfg config.Config) (*Clients, error) {
	computeClient, err := compute.NewService(ctx, option.WithScopes(compute.CloudPlatformScope))
	if err != nil {
		return nil, fmt.Errorf("create Compute client: %w", err)
	}
	var options []option.ClientOption
	if cfg.OSConfigEndpoint != "" {
		options = append(options, option.WithEndpoint(cfg.OSConfigEndpoint))
	}
	osconfigClient, err := osconfig.NewClient(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("create OS Config client: %w", err)
	}
	zonalClient, err := osconfig.NewOsConfigZonalClient(ctx, options...)
	if err != nil {
		_ = osconfigClient.Close()
		return nil, fmt.Errorf("create OS Config zonal client: %w", err)
	}
	return &Clients{compute: computeClient, osconfig: osconfigClient, zonal: zonalClient, poll: cfg.PollInterval}, nil
}

// Close releases client transports.
func (c *Clients) Close() error { return errors.Join(c.osconfig.Close(), c.zonal.Close()) }

// VM is a test-owned Compute Engine instance.
type VM struct {
	Project string `json:"project"`
	Zone    string `json:"zone"`
	Name    string `json:"name"`
	ID      uint64 `json:"id,string"`
}

// VMRequest contains only the inputs needed by the POC.
type VMRequest struct {
	Project          string
	Zone             string
	Name             string
	Image            string
	MachineType      string
	Network          string
	ServiceAccount   string
	EnableExternalIP bool
	Metadata         map[string]string
	Labels           map[string]string
}

// CreateVM creates a VM and waits for the zonal operation. Callers register
// cleanup by identity before invoking this method.
func (c *Clients) CreateVM(ctx context.Context, request VMRequest) (*VM, error) {
	var metadata []*compute.MetadataItems
	for key, value := range request.Metadata {
		value := value
		metadata = append(metadata, &compute.MetadataItems{Key: key, Value: &value})
	}
	networkInterface := &compute.NetworkInterface{Network: request.Network}
	if request.EnableExternalIP {
		networkInterface.AccessConfigs = []*compute.AccessConfig{{Name: "External NAT", Type: "ONE_TO_ONE_NAT"}}
	}
	instance := &compute.Instance{
		Name:        request.Name,
		MachineType: fmt.Sprintf("zones/%s/machineTypes/%s", request.Zone, request.MachineType),
		Labels:      request.Labels,
		Metadata:    &compute.Metadata{Items: metadata},
		NetworkInterfaces: []*compute.NetworkInterface{
			networkInterface,
		},
		Disks: []*compute.AttachedDisk{{
			AutoDelete: true,
			Boot:       true,
			InitializeParams: &compute.AttachedDiskInitializeParams{
				SourceImage: request.Image,
			},
		}},
		ServiceAccounts: []*compute.ServiceAccount{{
			Email:  request.ServiceAccount,
			Scopes: []string{compute.CloudPlatformScope},
		}},
	}
	op, err := c.compute.Instances.Insert(request.Project, request.Zone, instance).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("insert instance %s/%s/%s: %w", request.Project, request.Zone, request.Name, err)
	}
	if err := c.waitZoneOperation(ctx, request.Project, request.Zone, op.Name); err != nil {
		return nil, fmt.Errorf("create instance %s: %w", request.Name, err)
	}
	created, err := c.compute.Instances.Get(request.Project, request.Zone, request.Name).Context(ctx).Do()
	if err != nil {
		return nil, fmt.Errorf("get created instance %s: %w", request.Name, err)
	}
	return &VM{Project: request.Project, Zone: request.Zone, Name: request.Name, ID: created.Id}, nil
}

// DeleteVM idempotently deletes a VM and waits for completion.
func (c *Clients) DeleteVM(ctx context.Context, project, zone, name string) error {
	op, err := c.compute.Instances.Delete(project, zone, name).Context(ctx).Do()
	if isHTTPStatus(err, http.StatusNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete instance %s/%s/%s: %w", project, zone, name, err)
	}
	if err := c.waitZoneOperation(ctx, project, zone, op.Name); err != nil {
		return fmt.Errorf("wait for instance %s deletion: %w", name, err)
	}
	return nil
}

// SerialOutput retrieves port 1 from the beginning.
func (c *Clients) SerialOutput(ctx context.Context, vm VM) (string, error) {
	output, err := c.compute.Instances.GetSerialPortOutput(vm.Project, vm.Zone, vm.Name).Port(1).Start(0).Context(ctx).Do()
	if err != nil {
		return "", fmt.Errorf("get serial output for %s: %w", vm.Name, err)
	}
	return output.Contents, nil
}

// GuestAttribute returns one guest attribute value.
func (c *Clients) GuestAttribute(ctx context.Context, vm VM, queryPath, variableKey string) (string, error) {
	attributes, err := c.compute.Instances.GetGuestAttributes(vm.Project, vm.Zone, vm.Name).QueryPath(queryPath).VariableKey(variableKey).Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return attributes.VariableValue, nil
}

// Inventory returns the full OS Config inventory for a VM.
func (c *Clients) Inventory(ctx context.Context, vm VM) (*osconfigpb.Inventory, error) {
	name := fmt.Sprintf("projects/%s/locations/%s/instances/%d/inventory", vm.Project, vm.Zone, vm.ID)
	return c.zonal.GetInventory(ctx, &osconfigpb.GetInventoryRequest{Name: name, View: osconfigpb.InventoryView_FULL})
}

// CreateOSPolicyAssignment creates and waits for a zonal assignment.
func (c *Clients) CreateOSPolicyAssignment(ctx context.Context, project, zone, id string, assignment *osconfigpb.OSPolicyAssignment) (*osconfigpb.OSPolicyAssignment, error) {
	op, err := c.zonal.CreateOSPolicyAssignment(ctx, &osconfigpb.CreateOSPolicyAssignmentRequest{
		Parent:               fmt.Sprintf("projects/%s/locations/%s", project, zone),
		OsPolicyAssignmentId: id,
		OsPolicyAssignment:   assignment,
	})
	if err != nil {
		return nil, fmt.Errorf("create OS policy assignment %s: %w", id, err)
	}
	created, err := op.Wait(ctx)
	if err != nil {
		return nil, fmt.Errorf("wait for OS policy assignment %s creation: %w", id, err)
	}
	return created, nil
}

// DeleteOSPolicyAssignment idempotently deletes and waits for an assignment.
func (c *Clients) DeleteOSPolicyAssignment(ctx context.Context, name string) error {
	op, err := c.zonal.DeleteOSPolicyAssignment(ctx, &osconfigpb.DeleteOSPolicyAssignmentRequest{Name: name})
	if IsAPIStatus(err, codes.NotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete OS policy assignment %s: %w", name, err)
	}
	if err := op.Wait(ctx); err != nil && !IsAPIStatus(err, codes.NotFound) {
		return fmt.Errorf("wait for OS policy assignment %s deletion: %w", name, err)
	}
	return nil
}

// OSPolicyAssignmentReport gets one assignment report for an exact VM ID.
func (c *Clients) OSPolicyAssignmentReport(ctx context.Context, vm VM, assignmentID string) (*osconfigpb.OSPolicyAssignmentReport, error) {
	name := fmt.Sprintf("projects/%s/locations/%s/instances/%d/osPolicyAssignments/%s/report", vm.Project, vm.Zone, vm.ID, assignmentID)
	return c.zonal.GetOSPolicyAssignmentReport(ctx, &osconfigpb.GetOSPolicyAssignmentReportRequest{Name: name})
}

// ExecutePatchJob starts a project-scoped patch job.
func (c *Clients) ExecutePatchJob(ctx context.Context, request *osconfigpb.ExecutePatchJobRequest) (*osconfigpb.PatchJob, error) {
	return c.osconfig.ExecutePatchJob(ctx, request)
}

// PatchJob gets the latest state of a patch job.
func (c *Clients) PatchJob(ctx context.Context, name string) (*osconfigpb.PatchJob, error) {
	return c.osconfig.GetPatchJob(ctx, &osconfigpb.GetPatchJobRequest{Name: name})
}

// PatchJobInstanceDetails returns every instance result for a patch job.
func (c *Clients) PatchJobInstanceDetails(ctx context.Context, name string) ([]*osconfigpb.PatchJobInstanceDetails, error) {
	it := c.osconfig.ListPatchJobInstanceDetails(ctx, &osconfigpb.ListPatchJobInstanceDetailsRequest{Parent: name})
	var details []*osconfigpb.PatchJobInstanceDetails
	for {
		detail, err := it.Next()
		if err == iterator.Done {
			return details, nil
		}
		if err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
}

func (c *Clients) waitZoneOperation(ctx context.Context, project, zone, name string) error {
	var completed *compute.Operation
	err := poll.Until(ctx, c.poll, fmt.Sprintf("zonal operation %s", name), func(ctx context.Context) (string, bool, error) {
		op, err := c.compute.ZoneOperations.Get(project, zone, name).Context(ctx).Do()
		if err != nil {
			if IsTransientComputeError(err) {
				return fmt.Sprintf("transient operation read: %v", err), false, nil
			}
			return "", false, err
		}
		if op.Status != "DONE" {
			return fmt.Sprintf("status=%s progress=%d", op.Status, op.Progress), false, nil
		}
		completed = op
		return "status=DONE", true, nil
	})
	if err != nil {
		return err
	}
	if completed.Error != nil && len(completed.Error.Errors) > 0 {
		var messages []string
		for _, item := range completed.Error.Errors {
			messages = append(messages, fmt.Sprintf("%s: %s", item.Code, item.Message))
		}
		return fmt.Errorf("operation %s failed: %s", name, strings.Join(messages, "; "))
	}
	return nil
}

// IsTransientInventoryError identifies errors safe to treat as observations
// while waiting for eventual consistency.
func IsTransientInventoryError(err error) bool {
	switch status.Code(err) {
	case codes.NotFound, codes.Unavailable, codes.ResourceExhausted, codes.Internal, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// IsTransientOSConfigError identifies retryable OS Config read errors.
func IsTransientOSConfigError(err error) bool {
	switch status.Code(err) {
	case codes.NotFound, codes.Unavailable, codes.ResourceExhausted, codes.Internal, codes.DeadlineExceeded:
		return true
	default:
		return false
	}
}

// IsAPIStatus reports a gRPC API status code through wrapped errors.
func IsAPIStatus(err error, code codes.Code) bool { return status.Code(err) == code }

// IsNotFound reports an eventually consistent Compute API lookup.
func IsNotFound(err error) bool { return isHTTPStatus(err, http.StatusNotFound) }

// IsTransientComputeError identifies retryable read-side Compute API errors.
func IsTransientComputeError(err error) bool {
	var apiErr *googleapi.Error
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.Code == http.StatusTooManyRequests || apiErr.Code >= 500
}

func isHTTPStatus(err error, code int) bool {
	var apiErr *googleapi.Error
	return errors.As(err, &apiErr) && apiErr.Code == code
}
