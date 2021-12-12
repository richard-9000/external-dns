/*
Copyright 2020 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.

OpnSense HTTP Client from:
https://github.com/kradalby/opnsense-go
*/

package osb

import (
	"bytes"
	"strings"
	"encoding/json"
	"io/ioutil"
	"sigs.k8s.io/external-dns/plan"
	"fmt"
	"errors"
	"io"
	"context"
	"sigs.k8s.io/external-dns/endpoint"
	"net/url"
	"net/http"
	"time"
	"github.com/gobuffalo/envy"
	"sigs.k8s.io/external-dns/provider"
	"go.uber.org/ratelimit"
)

const (
	osbDefaultTTL = 60
)

var (
	ErrRecordToMutateNotFound = errors.New("record to mutate not found in current zone")
	ErrNoDryRun = errors.New("dry run not supported")
)

// OSBProvider is an implementation of Provider for OpnSense Bind DNS.
type OSBProvider struct {
	provider.BaseProvider
	client osbClient
	apiRateLimiter ratelimit.Limiter
	DryRun       bool
}

type osbClient interface {
	Get(api string) (resp []byte, code int, err error)
	Post(api string, body io.Reader) (resp []byte, code int, err error)
}

type OsbHttpClient struct {
	opnUrl *url.URL
	key string
	secret string
	c *http.Client
}

func (c *OsbHttpClient) Req(api string, action string, body io.Reader) (resp []byte, code int, err error) {
	url := c.opnUrl.String() + "/api/" + api
	r := []byte{}
	request, err := http.NewRequestWithContext(context.TODO(), action, url, body)
	if err != nil {
		return r, 0, err
	}
	request.SetBasicAuth(c.key, c.secret)
	httpresp, err := c.c.Do(request)
	if err != nil {
		return r, 0, err
	}
	defer httpresp.Body.Close()
	rbody, err := ioutil.ReadAll(httpresp.Body)
	return rbody, httpresp.StatusCode, nil
}

func (c *OsbHttpClient) Get(api string) (resp []byte, code int, err error) {
	return c.Req(api, "GET", nil)
}

func (c *OsbHttpClient) Post(api string, body io.Reader) (resp []byte, code int, err error) {
	return c.Req(api, "POST", body)
}

// NewOSBClientinitializes a new OSB DNS based Provider.
func NewOSBProvider(ctx context.Context, opnUrl, apiRateLimit int, dryRun bool) (*OSBProvider, error) {

	// TODO: Add Dry Run support
	if dryRun {
		return nil, ErrNoDryRun
	}

	urlp, err := url.Parse(envy.Get("OPNSENSEBIND_URL", opnUrl))
	if err != nil {
		return nil, err
	}
	httpClient := &http.Client{ Timeout: 60 * time.Second }
	client := OsbHttpClient {
		opnUrl: urlp,
		key: envy.Get("OPNSENSEBIND_KEY", key),
		secret: envy.Get("OPNSENSEBIND_SECRET", secret),
		c: httpClient,
	}
	_, _, err = client.Get("bind/record/get")
	if err != nil {
		return nil, err
	}

	return &OSBProvider {
		client:         &client,
		apiRateLimiter: ratelimit.New(apiRateLimit),
		DryRun:         dryRun,
	}, nil
}


type DNSRecords struct {
  Record DNSRecordsRecord `json:"record"`
}

type DNSRecordsRecord struct {
		Records DNSRecordsRecordRecords `json:"records"`
}

// This is absurd... nested types in golang don't work well for encoding/json
type DNSRecordsRecordRecords struct {
		Record map[string]DNSRecord `json:"record"`
}

type DNSRecord struct {
	Enabled json.Number `json:"enabled"`
	Name string `json:"name"`
	Value string `json:"value"`
	Domain map[string]DNSRecordDomain `json:"domain"`
	Type   map[string]DNSRecordType `json:"type"`
}

type DNSRecordDomain  struct  {
	Selected json.Number `json:"selected"`
	Value string `json:"value"`
}

type	DNSRecordType struct  {
	Selected json.Number `json:"selected"`
	Value string `json:"value"`
}

type DNSAddRequestRecord struct {
	Domain string `json:"domain"`
	Enabled json.Number `json:"enabled"`
	Value string `json:"value"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type DNSAddRequest struct {
  Record DNSAddRequestRecord `json:"record"`
}

type DNSDomains struct {
  Domain DNSDomainsDomain `json:"domain"`
}

type DNSDomainsDomain struct {
	Domains DNSDomainsDomainDomains `json:"domains"`
}

type DNSDomainsDomainDomains struct {
	Domain map[string]DNSDomain `json:"domain"`
}

type DNSDomain struct {
	Enabled json.Number `json:"enabled"`
	Name string `json:"domainname"`
	Type DNSDomainType `json:"type"`
}

type DNSDomainType struct{
	Master DNSDomainMaster `json:"master"`
	Slave  DNSDomainSlave `json:"slave"`
}

type DNSDomainMaster struct {
			Selected json.Number `json:"selected"`
}

type DNSDomainSlave struct {
			Selected json.Number `json:"selected"`
}

func (d DNSRecord) SelectedDomain() (string, string, error) {
	for k,v := range d.Domain {
		selval, err := v.Selected.Int64();
		if selval == 1 {
			return k, v.Value, err
		}
	}
	return "", "", nil
}

func (d DNSRecord) SelectedType() (string, error) {
	for k,v := range d.Type {
		selval, err := v.Selected.Int64();
		if selval == 1 {
			return k, err
		}
	}
	return "", nil
}

type GroupRecord struct {
	Name string
	Type string
}

type GroupTarget struct {
	RecordID string
	Value string
	Domain string
	DomainID string
	Record GroupRecord
}

func groupTargets(dnsRecords *DNSRecords) (map[GroupRecord][]GroupTarget, error) {
	records := make(map[GroupRecord][]GroupTarget)

	for k,v := range dnsRecords.Record.Records.Record {
		var r GroupRecord
		var t GroupTarget
		r.Name = v.Name
		recordDomainId, recordDomain, err := v.SelectedDomain()
		if err != nil {
			return nil, err
		}
		t.DomainID = recordDomainId
		t.Domain = recordDomain
		recordType, err := v.SelectedType()
		if err != nil {
			return nil, err
		}
		r.Type = recordType
		t.RecordID = k
		t.Value = v.Value
		t.Record = r
		records[r] = append(records[r], t)
	}
	return records, nil
}

func (p *OSBProvider) getZones() ([]*DNSDomain, error) {
	body, _, err := p.client.Get("bind/domain/get")
	if err != nil {
		return nil, err
	}

	var dnsDomains DNSDomains
	err = json.Unmarshal(body, &dnsDomains)
	if err != nil {
		return nil, err
	}

	var domains = []*DNSDomain {}
	for _,v := range dnsDomains.Domain.Domains.Domain {
			//TODO: filter out disabled zones and slaves
			domains = append(domains, &v)
	}
	return domains, nil
}

func (p *OSBProvider) getRecordsAndGroup() (map[GroupRecord][]GroupTarget, error) {
	body, _, err := p.client.Get("bind/record/get")
	if err != nil {
		return nil, err
	}

	var dnsRecords DNSRecords
	err = json.Unmarshal(body, &dnsRecords)
	if err != nil {
		return nil, err
	}

	return groupTargets(&dnsRecords)
}

// Records returns the list of records in all relevant zones.
func (p *OSBProvider) Records(ctx context.Context) ([]*endpoint.Endpoint, error) {
	// List all DNS records, note TTL is not part of this describe, unfortunately.
	records, err := p.getRecordsAndGroup()
	if err != nil {
		return nil, err
	}

	endpoints := []*endpoint.Endpoint{}
	for k,v := range records {
		vals := []string{}
		for _,val := range v {
			vals = append(vals, val.Value)
		}
		endpoint := endpoint.NewEndpointWithTTL(
			strings.TrimPrefix(k.Name+"."+v[0].Domain, "."),
			k.Type,
			osbDefaultTTL,
			vals...,
		)
		endpoints = append(endpoints, endpoint)
	}

	return endpoints, nil
}

func (p *OSBProvider) createRecords(endpoints []*endpoint.Endpoint) error {
	//Get all zones.
  zones, err := p.getZones()
	if err != nil {
		return err
	}

	for _,v := range endpoints {
		// Find the zone that best fits the request hostname
		var domainPicked int
		i := 0
		for zk, zv := range zones {
			if strings.HasSuffix(v.DNSName, zv.Name) && len(v.DNSName) > i {
				i = len(v.DNSName)
				domainPicked = zk
			}
		}

		if i == 0 {
			return ErrRecordToMutateNotFound
		}

		// Subtract the domain suffix from the requested hostname and clean it up.
		zn  := zones[domainPicked].Name
		dpl := len(zn)
		st  := len(v.DNSName)
		x   := st-dpl
		prefix := v.DNSName[0:x]

		for _, tv := range v.Targets {
			rec := &DNSAddRequestRecord{
					Domain: zones[domainPicked].Name,
					Enabled: json.Number("1"),
					Value: tv,
					Name: prefix,
					Type: v.RecordType,
			}

			req := &DNSAddRequest{ *rec }
			body, err := json.Marshal(&req)
			if err != nil {
				return err
			}
			_, _, err = p.client.Post("bind/record/addRecord", bytes.NewBuffer(body))
		}
	}
	return nil
}

func (p *OSBProvider) deleteRecords(delEndpoints []*endpoint.Endpoint, records map[GroupRecord][]GroupTarget) error {

	for _,v := range delEndpoints {
		var changeRecord GroupRecord
		changeRecord.Name = v.DNSName
		changeRecord.Type = v.RecordType

		if _, ok := records[changeRecord]; ok {
			// Execute delete using opnsense bind api
			for _, dv := range records[changeRecord] {
				_, _, err := p.client.Post(fmt.Sprintf("bind/record/delRecord/%s", dv.RecordID), nil)
				if err != nil {
					return err
				}
			}
		} else {
      return ErrRecordToMutateNotFound
		}
	}
	return nil
}

// ApplyChanges applies a given set of changes in a given zone.
func (p *OSBProvider) ApplyChanges(ctx context.Context, changes *plan.Changes) error {
	// Collect a list of uniq fqdns with attributes
	records, err := p.getRecordsAndGroup()
	if err != nil {
		return err
	}

	// Create
	err = p.createRecords(changes.Create)
	if err != nil {
		return err
	}
	err = p.createRecords(changes.UpdateNew)
	if err != nil {
		return err
	}

	// Delete
	err = p.deleteRecords(changes.UpdateOld, records)
	if err != nil {
		return err
	}
	err = p.deleteRecords(changes.Delete,    records)
	if err != nil {
		return err
	}
	return nil
}
