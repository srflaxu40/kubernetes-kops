/*
Copyright 2016 The Kubernetes Authors.

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

package aws

import (
	"fmt"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/golang/glog"
	"k8s.io/kops/upup/pkg/fi"
	"k8s.io/kops/upup/pkg/fi/cloudup/awsup"
	"k8s.io/kops/upup/pkg/fi/cloudup/spotinst"
)

//go:generate fitask -type=Route
type Route struct {
	Name      *string
	Lifecycle *fi.Lifecycle

	RouteTable *RouteTable
	CIDR       *string

	// Either an InternetGateway or a NAT Gateway
	// MUST be provided.
	InternetGateway *InternetGateway
	NatGateway      *NatGateway
}

func (e *Route) Find(c *fi.Context) (*Route, error) {
	cloud := c.Cloud.(spotinst.Cloud).Cloud().(awsup.AWSCloud)

	if e.RouteTable == nil || e.CIDR == nil {
		// TODO: Move to validate?
		return nil, nil
	}

	if e.RouteTable.ID == nil {
		return nil, nil
	}

	request := &ec2.DescribeRouteTablesInput{
		RouteTableIds: []*string{e.RouteTable.ID},
	}

	response, err := cloud.EC2().DescribeRouteTables(request)
	if err != nil {
		return nil, fmt.Errorf("error listing RouteTables: %v", err)
	}
	if response == nil || len(response.RouteTables) == 0 {
		return nil, nil
	} else {
		if len(response.RouteTables) != 1 {
			glog.Fatalf("found multiple RouteTables matching tags")
		}
		rt := response.RouteTables[0]
		for _, r := range rt.Routes {
			if aws.StringValue(r.DestinationCidrBlock) != *e.CIDR {
				continue
			}
			actual := &Route{
				Name:       e.Name,
				RouteTable: &RouteTable{ID: rt.RouteTableId},
				CIDR:       r.DestinationCidrBlock,
			}
			if r.GatewayId != nil {
				actual.InternetGateway = &InternetGateway{ID: r.GatewayId}
			}
			if r.NatGatewayId != nil {
				actual.NatGateway = &NatGateway{ID: r.NatGatewayId}
			}

			if aws.StringValue(r.State) == "blackhole" {
				glog.V(2).Infof("found route is a blackhole route")
				// These should be nil anyway, but just in case...
				actual.InternetGateway = nil
			}

			// Prevent spurious changes
			actual.Lifecycle = e.Lifecycle

			glog.V(2).Infof("found route matching cidr %s", *e.CIDR)
			return actual, nil
		}
	}

	return nil, nil
}

func (e *Route) Run(c *fi.Context) error {
	return fi.DefaultDeltaRunMethod(e, c)
}

func (s *Route) CheckChanges(a, e, changes *Route) error {
	if a == nil {
		// TODO: Create validate method?
		if e.RouteTable == nil {
			return fi.RequiredField("RouteTable")
		}
		if e.CIDR == nil {
			return fi.RequiredField("CIDR")
		}
		targetCount := 0
		if e.InternetGateway != nil {
			targetCount++
		}
		if e.NatGateway != nil {
			targetCount++
		}
		if targetCount == 0 {
			return fmt.Errorf("InternetGateway or NatGateway is required")
		}
		if targetCount != 1 {
			return fmt.Errorf("Cannot set more than 1 InternetGateway or NatGateway")
		}
	}

	if a != nil {
		if changes.RouteTable != nil {
			return fi.CannotChangeField("RouteTable")
		}
		if changes.CIDR != nil {
			return fi.CannotChangeField("CIDR")
		}
	}
	return nil
}

func (_ *Route) Render(t *spotinst.Target, a, e, changes *Route) error {
	if a == nil {
		request := &ec2.CreateRouteInput{}
		request.RouteTableId = checkNotNil(e.RouteTable.ID)
		request.DestinationCidrBlock = checkNotNil(e.CIDR)

		if e.InternetGateway == nil && e.NatGateway == nil {
			return fmt.Errorf("missing target for route")
		} else if e.InternetGateway != nil {
			request.GatewayId = checkNotNil(e.InternetGateway.ID)
		} else if e.NatGateway != nil {
			if err := e.NatGateway.waitAvailable(t.Cloud.(spotinst.Cloud).Cloud().(awsup.AWSCloud)); err != nil {
				return err
			}

			request.NatGatewayId = checkNotNil(e.NatGateway.ID)
		}

		glog.V(2).Infof("Creating Route with RouteTable:%q CIDR:%q", *e.RouteTable.ID, *e.CIDR)

		response, err := t.Cloud.(spotinst.Cloud).Cloud().(awsup.AWSCloud).EC2().CreateRoute(request)
		if err != nil {
			return fmt.Errorf("error creating Route: %v", err)
		}

		if !aws.BoolValue(response.Return) {
			return fmt.Errorf("create Route request failed: %v", response)
		}
	} else {
		request := &ec2.ReplaceRouteInput{}
		request.RouteTableId = checkNotNil(e.RouteTable.ID)
		request.DestinationCidrBlock = checkNotNil(e.CIDR)

		if e.InternetGateway == nil && e.NatGateway == nil {
			return fmt.Errorf("missing target for route")
		} else if e.InternetGateway != nil {
			request.GatewayId = checkNotNil(e.InternetGateway.ID)
		} else if e.NatGateway != nil {
			if err := e.NatGateway.waitAvailable(t.Cloud.(spotinst.Cloud).Cloud().(awsup.AWSCloud)); err != nil {
				return err
			}

			request.NatGatewayId = checkNotNil(e.NatGateway.ID)
		}

		glog.V(2).Infof("Updating Route with RouteTable:%q CIDR:%q", *e.RouteTable.ID, *e.CIDR)

		_, err := t.Cloud.(spotinst.Cloud).Cloud().(awsup.AWSCloud).EC2().ReplaceRoute(request)
		if err != nil {
			return fmt.Errorf("error updating Route: %v", err)
		}
	}

	return nil
}

func checkNotNil(s *string) *string {
	if s == nil {
		glog.Fatal("string pointer was unexpectedly nil")
	}
	return s
}
