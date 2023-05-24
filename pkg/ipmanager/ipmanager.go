package ipmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/api/compute/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type IpAddress struct {
	Name     string
	Address  string
	Assigned bool
}

type IpManager interface {
	Stop()
	IsAllowedIpAddress(string) bool
	AssignAllowedIpAddress(context.Context, string, string) (IpAddress, error)
}

type defaultIpManager struct {
	region         string
	project        string
	filter         string
	ipaddresses    []IpAddress
	quit           chan bool
	computeService *compute.Service
}

const (
	newAccessConfigName = "external-nat"
	newAccessConfigType = "ONE_TO_ONE_NAT"

	addressStatusInUse = "IN_USE"

	deleteAccessConfigPollingDeadline = 15 * time.Second
	addAccessConfigPollingDeadline    = 15 * time.Second
)

func InitIpManager(project string, region string, filter string, computeService *compute.Service, refreshInterval time.Duration) (*defaultIpManager, error) {
	im := defaultIpManager{ipaddresses: []IpAddress{}, region: region, project: project, filter: filter, computeService: computeService}
	ctx := context.Background()
	if err := im.refresh(ctx); err != nil {
		return nil, fmt.Errorf("Getting IP addresses failed, %w", err)
	}
	ticker := time.NewTicker(refreshInterval)
	go func() {
		for {
			select {
			case <-ticker.C:
				im.refresh(ctx)
			case <-im.quit:
				ticker.Stop()
				return
			}
		}
	}()
	return &im, nil
}

func (im *defaultIpManager) Stop() {
	im.quit <- true
}

func (im *defaultIpManager) refresh(ctx context.Context) error {
	log := log.FromContext(ctx)
	log.Info("Refreshing allowed IP address list", "filter", im.filter)

	ips := []IpAddress{}
	req := im.computeService.Addresses.List(im.project, im.region).Filter(im.filter)
	if err := req.Pages(ctx, func(page *compute.AddressList) error {
		for _, address := range page.Items {
			ip := IpAddress{Name: address.Name, Address: address.Address, Assigned: false}
			if address.Status == addressStatusInUse {
				ip.Assigned = true
			}
			ips = append(ips, ip)
		}
		return nil
	}); err != nil {
		log.Error(err, "failed to get addresses")
		return err
	}
	im.ipaddresses = ips
	return nil
}

func (im *defaultIpManager) IsAllowedIpAddress(ip string) bool {
	// No refresh since this lookup will happen a lot
	for _, address := range im.ipaddresses {
		if address.Address == ip {
			return true
		}
	}
	return false
}

func (im *defaultIpManager) getUnassignedIpAddress(ctx context.Context) (IpAddress, error) {
	im.refresh(ctx)
	for _, address := range im.ipaddresses {
		if !address.Assigned {
			return address, nil
		}
	}
	return IpAddress{}, errors.New("No available ip addresses in pool")
}

func (im *defaultIpManager) AssignAllowedIpAddress(ctx context.Context, nodeName string, zone string) (IpAddress, error) {
	log := log.FromContext(ctx)

	log.Info("Get compute instance")
	instance, err := im.computeService.Instances.Get(im.project, zone, nodeName).Context(ctx).Do()
	if err != nil {
		log.Error(err, "failed to get instance")
		return IpAddress{}, err
	}
	nic := instance.NetworkInterfaces[0] // todo: should we take multiple NIC's into consideration? AFAIK this is not possible on GKE.
	networkInterface := nic.Name

	if len(nic.AccessConfigs) != 0 {
		accessConfig := nic.AccessConfigs[0] // According to the docs there can only be 1

		// We can end up in this situation because we rely on node field status.addresses to check if we should assign a new IP address to a node.
		// This field has a few seconds delay vs the real world (GCP compute), so we need to do an extra check here and return if there is an allowed ip address assigned
		if im.IsAllowedIpAddress(accessConfig.NatIP) {
			log.Info("GCP check revealed allowed ip was already assigned")
			return IpAddress{}, nil
		}
	}
	// Check if there is an available IP before removing the current accessconfig
	ip, err := im.getUnassignedIpAddress(ctx)
	if err != nil {
		return IpAddress{}, err
	}

	if len(nic.AccessConfigs) != 0 {
		accessConfig := nic.AccessConfigs[0]
		log.Info("Removing accessconfig of instance", "accessConfigName", accessConfig.Name, "networkInterface", networkInterface)
		resp, err := im.computeService.Instances.DeleteAccessConfig(im.project, zone, nodeName, accessConfig.Name, networkInterface).Context(ctx).Do()
		if err != nil {
			log.Error(err, "failed to delete accessconfig on instance")
			return IpAddress{}, err
		}
		deleteAccessConfigCtx, cancel := context.WithTimeout(ctx, deleteAccessConfigPollingDeadline)
		defer cancel()
		err = im.waitForOperation(deleteAccessConfigCtx, zone, resp)
		if err != nil {
			log.Error(err, "delete accessconfig operation failed")
			return IpAddress{}, err
		}
	}

	ac := &compute.AccessConfig{
		Name:  newAccessConfigName,
		NatIP: ip.Address,
		Type:  newAccessConfigType,
	}
	log.Info("Adding accessconfig to instance", "ipAddress", ip.Address, "ipName", ip.Name, "accessConfigName", newAccessConfigName, "networkInterface", networkInterface)
	resp, err := im.computeService.Instances.AddAccessConfig(im.project, zone, nodeName, networkInterface, ac).Context(ctx).Do()
	if err != nil {
		log.Error(err, "failed to add ip to instance")
		return IpAddress{}, err
	}
	addAccessConfigCtx, cancel := context.WithTimeout(ctx, addAccessConfigPollingDeadline)
	defer cancel()
	err = im.waitForOperation(addAccessConfigCtx, zone, resp)
	if err != nil {
		log.Error(err, "addaccessconfig operation failed")
		return IpAddress{}, err
	}

	return ip, nil
}

func (im *defaultIpManager) waitForOperation(ctx context.Context, zone string, op *compute.Operation) error {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timeout waiting for operation to complete")
		case <-ticker.C:
			result, err := im.computeService.ZoneOperations.Get(im.project, zone, op.Name).Do()
			if err != nil {
				return fmt.Errorf("ZoneOperations.Get: %s", err)
			}

			if result.Status == "DONE" {
				if result.Error != nil {
					var errors []string
					for _, e := range result.Error.Errors {
						errors = append(errors, e.Message)
					}
					return fmt.Errorf("operation %q failed with error(s): %s", op.Name, strings.Join(errors, ", "))
				}
				return nil
			}
		}
	}
}
