/*
Copyright 2017 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package osb

import (
	"io"
	"context"
	"github.com/stretchr/testify/assert"
	"testing"
	"github.com/stretchr/testify/mock"
	"go.uber.org/ratelimit"
	"sigs.k8s.io/external-dns/endpoint"
	"sigs.k8s.io/external-dns/plan"
	"encoding/json"
)

type mockOsbClient struct {
	mock.Mock
}

func (c *mockOsbClient) Get(api string) (resp []byte, code int, err error) {
	stub := c.Called("/api/" + api)
	return stub.Get(0).([]byte), stub.Get(1).(int), stub.Error(2)
}

func (c *mockOsbClient) Post(api string, body io.Reader) (resp []byte, code int, err error) {
	stub := c.Called("/api/" + api)
	return stub.Get(0).([]byte), stub.Get(1).(int), stub.Error(2)
}

func TestOsbApplyChanges(t *testing.T) {
	assert := assert.New(t)
	client := new(mockOsbClient)
	provider := &OSBProvider{client: client, apiRateLimiter: ratelimit.New(10)}
	changes := plan.Changes{
		Create: []*endpoint.Endpoint{
			{DNSName: ".example.net", RecordType: "A", RecordTTL: 10, Targets: []string{"203.0.113.42"}},
		},
		Delete: []*endpoint.Endpoint{
			{DNSName: "osb.example.net", RecordType: "A", Targets: []string{"203.0.113.43"}},
		},
	}

	// Test and data for getting domain records
	records := &DNSRecords {
		Record: DNSRecordsRecord {
			Records: DNSRecordsRecordRecords {
				Record: map[string]DNSRecord {
					"123-456-789": {
						Enabled: "1",
						Name: "example.net",
						Value: "127.0.0.1",
						Domain:	map[string]DNSRecordDomain{
							"987-654-321": {
								Selected: json.Number("1"),
								Value: "example.net",
							},
						},
						Type: map[string]DNSRecordType{
							"A": {
								Selected: json.Number("1"),
								Value: "A",
							},
						},
					},
					"993-456-789": {
						Enabled: "1",
						Name: "osb.example.net",
						Value: "127.0.0.2",
						Domain:	map[string]DNSRecordDomain{
							"987-654-321": {
								Selected: json.Number("1"),
								Value: "example.net",
							},
						},
						Type: map[string]DNSRecordType{
							"A": {
								Selected: json.Number("1"),
								Value: "A",
							},
						},
					},
				},
			},
		},
	}
	body, _ := json.Marshal(records)
	client.On("Get", "/api/bind/record/get").Return(body, 200, nil).Once()

	// Test and data for getting zone records
	domains := &DNSDomains {
		Domain: DNSDomainsDomain {
			Domains: DNSDomainsDomainDomains {
				Domain: map[string]DNSDomain {
					"543-345-543": {
						Enabled: json.Number("1"),
						Name: "example.net",
						Type: DNSDomainType {
							Master: DNSDomainMaster {
								Selected: json.Number("1"),
							},
							Slave: DNSDomainSlave {
								Selected: json.Number("1"),
							},
						},
					},
				},
			},
		},
	}
	body, _ = json.Marshal(domains)
	client.On("Get", "/api/bind/domain/get").Return(body, 200, nil).Twice()
	//TODO add the actual response
	client.On("Post", "/api/bind/record/addRecord").Return([]byte{}, 200, nil).Once()
	client.On("Post", "/api/bind/record/delRecord/993-456-789").Return([]byte{}, 200, nil).Once()
	assert.NoError(provider.ApplyChanges(context.TODO(), &changes))
}

