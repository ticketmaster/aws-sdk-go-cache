package cache

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go/service/resourcegroupstaggingapi"

	"github.com/aws/aws-sdk-go/aws/request"

	"github.com/aws/aws-sdk-go/aws/credentials"

	"github.com/aws/aws-sdk-go/aws/endpoints"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/ec2"
	"github.com/aws/aws-sdk-go/service/elbv2"
)

func Test_Cachable(t *testing.T) {
	if !isCachable("DescribeTags") {
		t.Errorf("DescribeTags should be isCachable")
	}
	if !isCachable("ListTags") {
		t.Errorf("ListTags should be isCachable")
	}
	if !isCachable("GetSubnets") {
		t.Errorf("GetSubnets should be isCachable")
	}
	if isCachable("CreateTags") {
		t.Errorf("CreateTags should not be isCachable")
	}
}

var myCustomResolver = func(service, region string, optFns ...func(*endpoints.Options)) (endpoints.ResolvedEndpoint, error) {
	if service == endpoints.ElasticloadbalancingServiceID {
		return endpoints.ResolvedEndpoint{
			URL: server.URL,
		}, nil
	}
	if service == endpoints.Ec2ServiceID {
		return endpoints.ResolvedEndpoint{
			URL: server.URL + "/ec2",
		}, nil
	}
	if service == endpoints.TaggingServiceID {
		return endpoints.ResolvedEndpoint{
			URL: server.URL + "/tagging",
		}, nil
	}

	return endpoints.DefaultResolver().EndpointFor(service, region, optFns...)
}

var server *httptest.Server

func newSession() *session.Session {
	s := session.Must(session.NewSession(&aws.Config{
		Region:           aws.String("us-west-2"),
		EndpointResolver: endpoints.ResolverFunc(myCustomResolver),
		Credentials:      credentials.NewStaticCredentials("AKID", "SECRET_KEY", "TOKEN"),
	}))
	return s
}

func Test_CachedError(t *testing.T) {
	///ThrottledException: Rate exceeded
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.WriteHeader(400)
		rw.Write([]byte(`{ "code": "400", "message": "ThrottlingException"}`))
	}))
	defer server.Close()

	s := newSession()
	cacheCfg := NewConfig(10 * time.Second)
	AddCaching(s, cacheCfg)

	svc := resourcegroupstaggingapi.New(s)

	for i := 1; i < 10; i++ {
		req, _ := svc.GetResourcesRequest(&resourcegroupstaggingapi.GetResourcesInput{})
		err := req.Send()

		if err == nil {
			t.Errorf("400 error not received")
		}
		if IsCacheHit(req.HTTPRequest.Context()) {
			t.Errorf("400 error was received from cache")
		}
	}
}

func Test_Cache(t *testing.T) {
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Write(describeInstancesResponse)
	}))
	defer server.Close()

	s := newSession()
	cacheCfg := NewConfig(10 * time.Second)
	AddCaching(s, cacheCfg)

	svc := ec2.New(s)

	for i := 1; i < 10; i++ {
		descInstancesOutput, err := svc.DescribeInstances(
			&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
		if err != nil {
			t.Errorf("DescribeInstances returned an unexpected error %v", err)
		}

		if len(descInstancesOutput.Reservations) != 1 {
			t.Errorf("DescribeInstances did not return 1 reservation")
		}

		if len(descInstancesOutput.Reservations[0].Instances) != 1 {
			t.Errorf("DescribeInstances did not return 1 instance")
		}

		instanceId := "i-1234567890abcdef0"
		if aws.StringValue(descInstancesOutput.Reservations[0].Instances[0].InstanceId) != instanceId {
			t.Errorf("DescribeInstances returned InstanceId %v not %v",
				aws.StringValue(descInstancesOutput.Reservations[0].Instances[0].InstanceId), instanceId)
		}
	}
}

var cacheHit = false

func Test_CacheFlush(t *testing.T) {
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Write(describeInstancesResponse)
	}))
	defer server.Close()

	s := newSession()
	cacheCfg := NewConfig(10 * time.Second)
	AddCaching(s, cacheCfg)

	s.Handlers.Complete.PushBack(func(r *request.Request) {
		if IsCacheHit(r.HTTPRequest.Context()) != cacheHit {
			t.Errorf("DescribeInstances expected cache hit %v, got %v", IsCacheHit(r.Context()), cacheHit)
		}
	})

	svc := ec2.New(s)

	cacheHit = false
	_, err := svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}

	cacheHit = true
	_, err = svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}

	cacheCfg.FlushCache("ec2")
	cacheHit = false
	_, err = svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}
}

func Test_AutoCacheFlush(t *testing.T) {
	server = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		rw.Write(describeInstancesResponse)
	}))
	defer server.Close()

	s := newSession()
	cacheCfg := NewConfig(10 * time.Second)
	AddCaching(s, cacheCfg)

	s.Handlers.Complete.PushBack(func(r *request.Request) {
		if IsCacheHit(r.HTTPRequest.Context()) != cacheHit {
			t.Errorf("%v expected cache hit %v, got %v", r.Operation.Name, IsCacheHit(r.HTTPRequest.Context()), cacheHit)
		}
	})

	svc := ec2.New(s)

	cacheHit = false
	_, err := svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}

	cacheHit = true
	_, err = svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}

	// Make non Get/Describe/List query, should flush ec2 cache
	cacheHit = false
	_, err = svc.CreateKeyPair(&ec2.CreateKeyPairInput{KeyName: aws.String("name")})
	if err != nil {
		t.Errorf("CreateKeyPair returned an unexpected error %v", err)
	}

	cacheHit = false
	_, err = svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}

	// Make non Get/Describe/List query to non-ec2 service, should not flush ec2 cache
	cacheHit = false
	elbv2svc := elbv2.New(s)
	_, err = elbv2svc.DeleteLoadBalancer(&elbv2.DeleteLoadBalancerInput{LoadBalancerArn: aws.String("arn")})
	if err != nil {
		t.Errorf("DeleteLoadBalancer returned an unexpected error %v", err)
	}

	cacheHit = true
	_, err = svc.DescribeInstances(
		&ec2.DescribeInstancesInput{InstanceIds: []*string{aws.String("i-0ace172143b1159d6")}})
	if err != nil {
		t.Errorf("DescribeInstances returned an unexpected error %v", err)
	}
}

var describeInstancesResponse = []byte(`<DescribeInstancesResponse xmlns="http://ec2.amazonaws.com/doc/2016-11-15/">
    <requestId>8f7724cf-496f-496e-8fe3-example</requestId>
    <reservationSet>
        <item>
            <reservationId>r-1234567890abcdef0</reservationId>
            <ownerId>123456789012</ownerId>
            <groupSet/>
            <instancesSet>
                <item>
                    <instanceId>i-1234567890abcdef0</instanceId>
                    <imageId>ami-bff32ccc</imageId>
                    <instanceState>
                        <code>16</code>
                        <name>running</name>
                    </instanceState>
                    <privateDnsName>ip-192-168-1-88.eu-west-1.compute.internal</privateDnsName>
                    <dnsName>ec2-54-194-252-215.eu-west-1.compute.amazonaws.com</dnsName>
                    <reason/>
                    <keyName>my_keypair</keyName>
                    <amiLaunchIndex>0</amiLaunchIndex>
                    <productCodes/>
                    <instanceType>t2.micro</instanceType>
                    <launchTime>2018-05-08T16:46:19.000Z</launchTime>
                    <placement>
                        <availabilityZone>eu-west-1c</availabilityZone>
                        <groupName/>
                        <tenancy>default</tenancy>
                    </placement>
                    <monitoring>
                        <state>disabled</state>
                    </monitoring>
                    <subnetId>subnet-56f5f633</subnetId>
                    <vpcId>vpc-11112222</vpcId>
                    <privateIpAddress>192.168.1.88</privateIpAddress>
                    <ipAddress>54.194.252.215</ipAddress>
                    <sourceDestCheck>true</sourceDestCheck>
                    <groupSet>
                        <item>
                            <groupId>sg-e4076980</groupId>
                            <groupName>SecurityGroup1</groupName>
                        </item>
                    </groupSet>
                    <architecture>x86_64</architecture>
                    <rootDeviceType>ebs</rootDeviceType>
                    <rootDeviceName>/dev/xvda</rootDeviceName>
                    <blockDeviceMapping>
                        <item>
                            <deviceName>/dev/xvda</deviceName>
                            <ebs>
                                <volumeId>vol-1234567890abcdef0</volumeId>
                                <status>attached</status>
                                <attachTime>2015-12-22T10:44:09.000Z</attachTime>
                                <deleteOnTermination>true</deleteOnTermination>
                            </ebs>
                        </item>
                    </blockDeviceMapping>
                    <virtualizationType>hvm</virtualizationType>
                    <clientToken>xMcwG14507example</clientToken>
                    <tagSet>
                        <item>
                            <key>Name</key>
                            <value>Server_1</value>
                        </item>
                    </tagSet>
                    <hypervisor>xen</hypervisor>
                    <networkInterfaceSet>
                        <item>
                            <networkInterfaceId>eni-551ba033</networkInterfaceId>
                            <subnetId>subnet-56f5f633</subnetId>
                            <vpcId>vpc-11112222</vpcId>
                            <description>Primary network interface</description>
                            <ownerId>123456789012</ownerId>
                            <status>in-use</status>
                            <macAddress>02:dd:2c:5e:01:69</macAddress>
                            <privateIpAddress>192.168.1.88</privateIpAddress>
                            <privateDnsName>ip-192-168-1-88.eu-west-1.compute.internal</privateDnsName>
                            <sourceDestCheck>true</sourceDestCheck>
                            <groupSet>
                                <item>
                                    <groupId>sg-e4076980</groupId>
                                    <groupName>SecurityGroup1</groupName>
                                </item>
                            </groupSet>
                            <attachment>
                                <attachmentId>eni-attach-39697adc</attachmentId>
                                <deviceIndex>0</deviceIndex>
                                <status>attached</status>
                                <attachTime>2018-05-08T16:46:19.000Z</attachTime>
                                <deleteOnTermination>true</deleteOnTermination>
                            </attachment>
                            <association>
                                <publicIp>54.194.252.215</publicIp>
                                <publicDnsName>ec2-54-194-252-215.eu-west-1.compute.amazonaws.com</publicDnsName>
                                <ipOwnerId>amazon</ipOwnerId>
                            </association>
                            <privateIpAddressesSet>
                                <item>
                                    <privateIpAddress>192.168.1.88</privateIpAddress>
                                    <privateDnsName>ip-192-168-1-88.eu-west-1.compute.internal</privateDnsName>
                                    <primary>true</primary>
                                    <association>
                                    <publicIp>54.194.252.215</publicIp>
                                    <publicDnsName>ec2-54-194-252-215.eu-west-1.compute.amazonaws.com</publicDnsName>
                                    <ipOwnerId>amazon</ipOwnerId>
                                    </association>
                                </item>
                            </privateIpAddressesSet>
                            <ipv6AddressesSet>
                               <item>
                                   <ipv6Address>2001:db8:1234:1a2b::123</ipv6Address>
                               </item>
                           </ipv6AddressesSet>
                        </item>
                    </networkInterfaceSet>
                    <iamInstanceProfile>
                        <arn>arn:aws:iam::123456789012:instance-profile/AdminRole</arn>
                        <id>ABCAJEDNCAA64SSD123AB</id>
                    </iamInstanceProfile>
                    <ebsOptimized>false</ebsOptimized>
                    <cpuOptions>
                        <coreCount>1</coreCount>
                        <threadsPerCore>1</threadsPerCore>
                    </cpuOptions>
                </item>
            </instancesSet>
        </item>
    </reservationSet>
</DescribeInstancesResponse>`)
