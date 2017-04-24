package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"os"

	"io"
	"io/ioutil"

	"github.com/Azure/azure-sdk-for-go/arm/compute"
	"github.com/Azure/azure-sdk-for-go/arm/network"
	"github.com/Azure/azure-sdk-for-go/arm/resources/resources"
	"github.com/Azure/go-autorest/autorest"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/to"
	"github.com/marstr/guid"
)

var (
	userSubscriptionID string
	userTenantID       string
	environment        = azure.PublicCloud
)

var (
	errLog    *log.Logger
	statusLog *log.Logger
	debugLog  *log.Logger
)

const (
	clientID = "04b07795-8ddb-461a-bbee-02f9e1bf7b46" // This is the client ID for the Azure CLI. It was chosen for its public well-known status.
	location = "WESTUS2"
)

func main() {
	var group resources.Group
	var sampleVM compute.VirtualMachine
	var authorizer autorest.Authorizer
	exitStatus := 1
	defer func() {
		os.Exit(exitStatus)
	}()

	debugLog.Println("Using Subscription ID: ", userSubscriptionID)
	debugLog.Println("Using Tenant ID: ", userTenantID)

	// Get authenticated so we can access the subscription used to run this sample.
	if temp, err := authenticate(userTenantID); err == nil {
		authorizer = temp
	} else {
		errLog.Printf("could not authenticate. Error: %v", err)
		return
	}

	// Create a Resource Group to act as a sandbox for this sample.
	if temp, deleter, err := setupResourceGroup(userSubscriptionID, authorizer); err == nil {
		group = temp
		statusLog.Print("Created Resource Group: ", *group.Name)
		defer deleter()
	} else {
		errLog.Printf("could not create resource group. Error: %v", err)
	}

	// Create an Azure Virtual Machine, on which we'll install an extension.
	if temp, err := setupVirtualMachine(userSubscriptionID, *group.Name, authorizer, nil); err == nil {
		sampleVM = temp
		statusLog.Print("Created Virtual Machine: ", *sampleVM.Name)
	} else {
		errLog.Print(err)
		return
	}

	statusLog.Print(*sampleVM.Name)

	exitStatus = 0
}

func init() {
	var badArgs bool

	errLog = log.New(os.Stderr, "[ERROR] ", 0)
	statusLog = log.New(os.Stdout, "[STATUS] ", 0)

	unformattedSubscriptionID := flag.String("subscription", os.Getenv("AZURE_SUBSCRIPTION_ID"), "The subscription that will be targeted when running this sample.")
	unformattedTenantID := flag.String("tenant", os.Getenv("AZURE_TENANT_ID"), "The tenant that hosts the subscription to be used by this sample.")
	printDebug := flag.Bool("debug", false, "Include debug information in the output of this program.")
	flag.Parse()

	ensureGUID := func(name, raw string) string {
		var retval string
		if parsed, err := guid.Parse(raw); err == nil {
			retval = parsed.String()
		} else {
			errLog.Printf("'%s' doesn't look like an Azure %s. This sample expects a uuid.", raw, name)
			badArgs = true
		}
		return retval
	}

	userSubscriptionID = ensureGUID("Subscription ID", *unformattedSubscriptionID)
	userTenantID = ensureGUID("Tenant ID", *unformattedTenantID)

	var debugWriter io.Writer
	if *printDebug {
		debugWriter = os.Stdout
	} else {
		debugWriter = ioutil.Discard
	}
	debugLog = log.New(debugWriter, "[DEBUG] ", 0)

	if badArgs {
		os.Exit(1)
	}
}

func setupResourceGroup(subscriptionID string, authorizer autorest.Authorizer) (created resources.Group, deleter func(), err error) {
	resourceClient := resources.NewGroupsClient(subscriptionID)
	resourceClient.Authorizer = authorizer

	created, err = resourceClient.CreateOrUpdate(getTempResourceGroupName(), resources.Group{
		Location: to.StringPtr(location),
	})

	if err == nil {
		deleter = func() {
			resourceClient.Delete(*created.Name, nil)
		}
	} else {
		deleter = func() {}
	}

	return
}

func setupVirtualMachine(subscriptionID, resourceGroup string, authorizer autorest.Authorizer, cancel <-chan struct{}) (created compute.VirtualMachine, err error) {
	client := compute.NewVirtualMachinesClient(subscriptionID)
	client.Authorizer = authorizer

	var netAccess network.Interface

	netAccess, err = setupNetworkInterface(subscriptionID, resourceGroup, authorizer)
	if err != nil {
		return
	}

	vmName := fmt.Sprintf("sample-vm%s", guid.NewGUID().Stringf(guid.FormatN))

	arguments := compute.VirtualMachine{
		Location: to.StringPtr(location),
		VirtualMachineProperties: &compute.VirtualMachineProperties{
			HardwareProfile: &compute.HardwareProfile{
				VMSize: compute.BasicA0,
			},
			OsProfile: &compute.OSProfile{
				ComputerName:  to.StringPtr(vmName),
				AdminUsername: to.StringPtr("admin"),
				AdminPassword: to.StringPtr("azureRocksWithGo"),
				LinuxConfiguration: &compute.LinuxConfiguration{
					DisablePasswordAuthentication: to.BoolPtr(false),
				},
			},
			NetworkProfile: &compute.NetworkProfile{
				NetworkInterfaces: &[]compute.NetworkInterfaceReference{
					compute.NetworkInterfaceReference{
						ID: netAccess.ID,
					},
				},
			},
		},
	}

	if _, err = client.CreateOrUpdate(resourceGroup, vmName, arguments, cancel); err == nil {
		created, err = client.Get(resourceGroup, vmName, compute.InstanceView)
	}
	return
}

func setupNetworkInterface(subscriptionID, resourceGroup string, authorizer autorest.Authorizer) (created network.Interface, err error) {
	client := network.NewInterfacesClient(subscriptionID)
	client.Authorizer = authorizer

	arguments := network.Interface{
		Location: to.StringPtr(location),
		InterfacePropertiesFormat: &network.InterfacePropertiesFormat{
			IPConfigurations: &[]network.InterfaceIPConfiguration{
				network.InterfaceIPConfiguration{
					InterfaceIPConfigurationPropertiesFormat: &network.InterfaceIPConfigurationPropertiesFormat{},
				},
			},
		},
	}

	name := "sample-networkInterface"

	_, err = client.CreateOrUpdate(resourceGroup, name, arguments, nil)
	if err != nil {
		return
	}

	created, err = client.Get(resourceGroup, name, "")
	return
}

func setupIPConfiguration(subscriptionID string, authorizer autorest.Authorizer) (network.InterfaceIPConfiguration, error) {
	return network.InterfaceIPConfiguration{}, errors.New("not implemented")
}

// getTempResourceGroupName generates a name of a resource group name that will not conflict with other resource groups.
func getTempResourceGroupName() string {
	randID := guid.NewGUID()

	return fmt.Sprintf("sample-rg%s", randID.Stringf(guid.FormatN))
}

// authenticate gets an authorization token to allow clients to access Azure assets.
func authenticate(tenantID string) (autorest.Authorizer, error) {
	authClient := autorest.NewClientWithUserAgent("github.com/Azure-Samples/arm-compute-go-vm-extensions")
	var deviceCode *azure.DeviceCode
	var token *azure.Token
	var config *azure.OAuthConfig

	if temp, err := environment.OAuthConfigForTenant(tenantID); err == nil {
		config = temp
	} else {
		return nil, err
	}

	debugLog.Print("DeviceCodeEndpoint: ", config.DeviceCodeEndpoint.String())
	if temp, err := azure.InitiateDeviceAuth(&authClient, *config, clientID, environment.ServiceManagementEndpoint); err == nil {
		deviceCode = temp
	} else {
		return nil, err
	}

	if _, err := fmt.Println(*deviceCode.Message); err != nil {
		return nil, err
	}

	if temp, err := azure.WaitForUserCompletion(&authClient, deviceCode); err == nil {
		token = temp
	} else {
		return nil, err
	}

	return token, nil
}
