package ipmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/api/compute/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func writeResp(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "unable to marshal request: "+err.Error(), http.StatusBadRequest)
		return
	}
	w.Write(b)
	return
}

func getAddressListHandler(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	query := r.URL.Query()
	if path == "/real-project/regions/real-region/addresses" && len(query["filter"]) == 1 {
		addresses := []*compute.Address{}
		if query["filter"][0] == "name eq ip-pool-a-.*" {
			addresses = []*compute.Address{
				{Address: "127.0.0.1", Name: "ip-pool-a-1", Status: "RESERVED"},
				{Address: "127.0.0.2", Name: "ip-pool-a-2", Status: "IN_USE"},
			}
		}
		if query["filter"][0] == "name eq ip-pool-b-.*" {
			addresses = []*compute.Address{
				{Address: "127.0.0.1", Name: "ip-pool-b-1", Status: "IN_USE"},
				{Address: "127.0.0.2", Name: "ip-pool-b-2", Status: "IN_USE"},
			}
		}
		resp := &compute.AddressList{
			Id:    "id",
			Items: addresses,
			Kind:  "compute#address",
		}
		writeResp(w, resp)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	writeResp(w, googleapi.Error{Code: http.StatusNotFound, Message: "Project not found"})
}

func TestIpManagerInit(t *testing.T) {
	tt := []struct {
		name     string
		project  string
		region   string
		filter   string
		wantData []IpAddress
		wantErr  bool
	}{
		{
			name:     "filter 1",
			project:  "real-project",
			region:   "real-region",
			filter:   "name eq ip-pool-a-.*",
			wantData: []IpAddress{{Name: "ip-pool-a-1", Address: "127.0.0.1", Assigned: false}, {Name: "ip-pool-a-2", Address: "127.0.0.2", Assigned: true}},
			wantErr:  false,
		},
		{
			name:     "no matches",
			project:  "real-project",
			region:   "real-region",
			filter:   "name eq NO MATCHES",
			wantData: []IpAddress{},
			wantErr:  false,
		},
		{
			name:     "project does not exist",
			project:  "DOESNOTEXIST",
			region:   "",
			filter:   "name eq NO MATCHES",
			wantData: []IpAddress{},
			wantErr:  true,
		},
		{
			name:     "region does not exist",
			project:  "real-project",
			region:   "DOESNOTEXIST",
			filter:   "name eq NO MATCHES",
			wantData: []IpAddress{},
			wantErr:  true,
		},
	}
	ctx := context.Background()

	ts := httptest.NewServer(http.HandlerFunc(getAddressListHandler))
	defer ts.Close()
	svc, err := compute.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(ts.URL))
	if err != nil {
		t.Fatalf("unable to create client: %v", err)
	}

	for i := range tt {
		tc := tt[i]

		t.Run(tc.name, func(t *testing.T) {
			im, err := InitIpManager(tc.project, tc.region, tc.filter, svc, 30*time.Minute)
			if tc.wantErr {
				require.NotNil(t, err)
				return
			} else {
				require.Nil(t, err)
			}
			if !reflect.DeepEqual(im.ipaddresses, tc.wantData) {
				t.Fatalf(`want: %v, got: %v`, tc.wantData, im.ipaddresses)
			}

		})
	}
}

func TestIpManagerIsAllowedIpAddress(t *testing.T) {
	project := "real-project"
	region := "real-region"
	filter := "name eq ip-pool-a-.*"
	tt := []struct {
		name    string
		testIP  string
		allowed bool
	}{
		{
			name:    "allowed",
			testIP:  "127.0.0.1",
			allowed: true,
		},
		{
			name:    "not allowed",
			testIP:  "127.0.0.3",
			allowed: false,
		},
	}
	ctx := context.Background()
	ts := httptest.NewServer(http.HandlerFunc(getAddressListHandler))
	defer ts.Close()
	svc, err := compute.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(ts.URL))
	if err != nil {
		t.Fatalf("unable to create client: %v", err)
	}

	for i := range tt {
		tc := tt[i]

		t.Run(tc.name, func(t *testing.T) {
			im, err := InitIpManager(project, region, filter, svc, 30*time.Minute)
			require.Nil(t, err)
			require.Equal(t, tc.allowed, im.IsAllowedIpAddress(tc.testIP))
		})
	}
}

func TestIpManagerUnassignedIpAddresses(t *testing.T) {
	tt := []struct {
		name     string
		project  string
		region   string
		filter   string
		wantData IpAddress
		wantErr  bool
	}{
		{
			name:     "filter a",
			project:  "real-project",
			region:   "real-region",
			filter:   "name eq ip-pool-a-.*",
			wantData: IpAddress{Name: "ip-pool-a-1", Address: "127.0.0.1", Assigned: false},
			wantErr:  false,
		},
		{
			name:     "filter b",
			project:  "real-project",
			region:   "real-region",
			filter:   "name eq ip-pool-b.*",
			wantData: IpAddress{},
			wantErr:  true,
		},
	}
	ctx := context.Background()
	ts := httptest.NewServer(http.HandlerFunc(getAddressListHandler))
	defer ts.Close()
	svc, err := compute.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(ts.URL))
	if err != nil {
		t.Fatalf("unable to create client: %v", err)
	}

	for i := range tt {
		tc := tt[i]

		t.Run(tc.name, func(t *testing.T) {
			im, err := InitIpManager(tc.project, tc.region, tc.filter, svc, 30*time.Minute)
			require.Nil(t, err)

			ipaddress, err := im.getUnassignedIpAddress(ctx)
			if tc.wantErr {
				require.NotNil(t, err)
				return
			} else {
				require.Nil(t, err)
			}
			if !reflect.DeepEqual(ipaddress, tc.wantData) {
				t.Fatalf(`want: %v, got: %v`, tc.wantData, im.ipaddresses)
			}

		})
	}
}

type IpManagerAssignIpAddressTestCase struct {
	name                                string
	project                             string
	region                              string
	filter                              string
	zone                                string
	nodeName                            string
	accessConfigPresent                 bool
	correctIpAlreadyAssigned            bool
	instanceNotFound                    bool
	deleteAccessConfigFails             bool
	addAccessConfigFails                bool
	wantErr                             bool
	expectedAddAccessConfigCallCount    int
	expectedDeleteAccessConfigCallCount int
}

func getInstanceHandler(tc IpManagerAssignIpAddressTestCase, w http.ResponseWriter, r *http.Request) {
	switch {
	case !tc.instanceNotFound && r.URL.Path == fmt.Sprintf("/%s/zones/%s/instances/%s", tc.project, tc.zone, tc.nodeName):
		i := compute.Instance{
			Kind: "compute#instance",
			Id:   123123123,
			Name: tc.nodeName,
			NetworkInterfaces: []*compute.NetworkInterface{{
				Name: "nic0",
				Kind: "compute#networkInterface",
			}},
		}
		if tc.accessConfigPresent {
			ip := "121.121.121.121"
			if tc.correctIpAlreadyAssigned {
				ip = "127.0.0.1"
			}
			i.NetworkInterfaces[0].AccessConfigs = []*compute.AccessConfig{{
				Name:  "external-nat",
				Kind:  "compute#accessConfig",
				NatIP: ip,
				Type:  "ONE_TO_ONE_NAT",
			}}
		}
		writeResp(w, i)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	writeResp(w, googleapi.Error{Code: http.StatusNotFound, Message: "Project not found"})
}

func deleteAccessConfigHandler(tc IpManagerAssignIpAddressTestCase, w http.ResponseWriter, r *http.Request) {
	switch {
	case tc.deleteAccessConfigFails:
		w.WriteHeader(http.StatusUnauthorized)
		writeResp(w, googleapi.Error{Code: http.StatusUnauthorized, Message: "Unauthorized"})
		return
	case r.URL.Path == fmt.Sprintf("/%s/zones/%s/instances/%s/deleteAccessConfig", tc.project, tc.zone, tc.nodeName):
		o := compute.Operation{
			Name:   "deleteoperation",
			Status: "PENDING",
		}
		writeResp(w, o)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	writeResp(w, googleapi.Error{Code: http.StatusNotFound, Message: "Project not found"})
}

func addAccessConfigHandler(tc IpManagerAssignIpAddressTestCase, w http.ResponseWriter, r *http.Request) {
	switch {
	case tc.addAccessConfigFails:
		w.WriteHeader(http.StatusUnauthorized)
		writeResp(w, googleapi.Error{Code: http.StatusUnauthorized, Message: "Unauthorized"})
		return
	case r.URL.Path == fmt.Sprintf("/%s/zones/%s/instances/%s/addAccessConfig", tc.project, tc.zone, tc.nodeName):
		o := compute.Operation{
			Name:   "addoperation",
			Status: "PENDING",
		}
		writeResp(w, o)
		return
	}
	w.WriteHeader(http.StatusNotFound)
	writeResp(w, googleapi.Error{Code: http.StatusNotFound, Message: "Project not found"})
}

func operationConfigHandler(tc IpManagerAssignIpAddressTestCase, w http.ResponseWriter, r *http.Request) {
	o := compute.Operation{
		Status: "DONE",
	}
	switch r.URL.Path {
	case fmt.Sprintf("/%s/zones/%s/operations/addoperation", tc.project, tc.zone):
		o.Name = "addoperation"
	case fmt.Sprintf("/%s/zones/%s/operations/deleteoperation", tc.project, tc.zone):
		o.Name = "deleteoperation"
	default:
		w.WriteHeader(http.StatusNotFound)
		writeResp(w, googleapi.Error{Code: http.StatusNotFound, Message: "Not found"})
	}

	writeResp(w, o)
}

func TestIpManagerAssignIpAddress(t *testing.T) {
	tt := []IpManagerAssignIpAddressTestCase{
		{
			name:                                "accessconfig present",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 true,
			correctIpAlreadyAssigned:            false,
			wantErr:                             false,
			expectedAddAccessConfigCallCount:    1,
			expectedDeleteAccessConfigCallCount: 1,
		},
		{
			name:                                "accessconfig present with correct ip",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 true,
			correctIpAlreadyAssigned:            true,
			wantErr:                             false,
			expectedAddAccessConfigCallCount:    0,
			expectedDeleteAccessConfigCallCount: 0,
		},
		{
			name:                                "accessconfig not present",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 false,
			correctIpAlreadyAssigned:            false,
			wantErr:                             false,
			expectedAddAccessConfigCallCount:    1,
			expectedDeleteAccessConfigCallCount: 0,
		},
		{
			name:                                "node not found",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "instance-does-not-exist",
			instanceNotFound:                    true,
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 false,
			correctIpAlreadyAssigned:            false,
			wantErr:                             true,
			expectedAddAccessConfigCallCount:    0,
			expectedDeleteAccessConfigCallCount: 0,
		},
		{
			name:                                "deleteaccessconfig failed",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 true,
			correctIpAlreadyAssigned:            false,
			deleteAccessConfigFails:             true,
			wantErr:                             true,
			expectedAddAccessConfigCallCount:    0,
			expectedDeleteAccessConfigCallCount: 1,
		},
		{
			name:                                "addaccessconfig failed",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-a-.*",
			accessConfigPresent:                 true,
			correctIpAlreadyAssigned:            false,
			addAccessConfigFails:                true,
			wantErr:                             true,
			expectedAddAccessConfigCallCount:    1,
			expectedDeleteAccessConfigCallCount: 1,
		},
		{
			name:                                "no ips available",
			project:                             "real-project",
			region:                              "real-region",
			zone:                                "real-region-a",
			nodeName:                            "real-instance-1",
			filter:                              "name eq ip-pool-b-.*",
			accessConfigPresent:                 true,
			correctIpAlreadyAssigned:            false,
			wantErr:                             true,
			expectedAddAccessConfigCallCount:    0,
			expectedDeleteAccessConfigCallCount: 0,
		},
	}

	for i := range tt {
		tc := tt[i]

		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			deleteAccessConfigCallCount := 0
			addAccessConfigCallCount := 0
			getInstanceConfigCallCount := 0
			ts := httptest.NewServer(http.HandlerFunc(
				func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.URL.Path == fmt.Sprintf("/%s/regions/%s/addresses", tc.project, tc.region):
						getAddressListHandler(w, r)
					case strings.Contains(r.URL.Path, "deleteAccessConfig"):
						deleteAccessConfigCallCount += 1
						deleteAccessConfigHandler(tc, w, r)
					case strings.Contains(r.URL.Path, "addAccessConfig"):
						addAccessConfigCallCount += 1
						addAccessConfigHandler(tc, w, r)
					case strings.Contains(r.URL.Path, "operations"):
						operationConfigHandler(tc, w, r)
					case strings.HasPrefix(r.URL.Path, fmt.Sprintf("/%s/zones/%s/instances/", tc.project, tc.zone)):
						getInstanceConfigCallCount += 1
						getInstanceHandler(tc, w, r)
					default:
						http.NotFoundHandler().ServeHTTP(w, r)
					}
				},
			))
			defer ts.Close()
			svc, err := compute.NewService(ctx, option.WithoutAuthentication(), option.WithEndpoint(ts.URL))
			if err != nil {
				t.Fatalf("unable to create client: %v", err)
			}
			im, err := InitIpManager(tc.project, tc.region, tc.filter, svc, 30*time.Minute)
			require.Nil(t, err)

			_, err = im.AssignAllowedIpAddress(ctx, tc.nodeName, tc.zone)
			if tc.wantErr {
				require.NotNil(t, err)
			} else {
				require.Nil(t, err)
			}
			require.Equal(t, 1, getInstanceConfigCallCount)
			require.Equal(t, tc.expectedAddAccessConfigCallCount, addAccessConfigCallCount)
			require.Equal(t, tc.expectedDeleteAccessConfigCallCount, deleteAccessConfigCallCount)
		})
	}
}
