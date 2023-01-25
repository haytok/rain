// Forecast looks at your account and tries to predict things that will
// go wrong when you attempt to CREATE, UPDATE, or DELETE a stack
package forecast

import (
	"fmt"
	"io"
	"os"

	"github.com/aws-cloudformation/rain/cft"
	"github.com/aws-cloudformation/rain/cft/parse"
	"github.com/aws-cloudformation/rain/internal/aws/cfn"
	"github.com/aws-cloudformation/rain/internal/cmd/deploy"
	"github.com/aws-cloudformation/rain/internal/config"
	"github.com/aws-cloudformation/rain/internal/ui"
	"github.com/spf13/cobra"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
)

// The role name to use for the IAM policy simulator (optional --role)
var RoleArn string

// The resource type to check (optional --type to limit checks to one type)
var ResourceType string

// The optional parameters to use to create a change set for update predictions (--params)
var Params []string

// The optional path to a file that contains params (--config)
var ConfigFilePath string

// Input to forecast prediction functions
type PredictionInput struct {
	source      cft.Template
	stackName   string
	resource    interface{}
	logicalId   string
	stackExists bool
	stack       types.Stack
}

// Query the account to make predictions about deployment failures.
// Returns true if no failures are predicted.
func predict(source cft.Template, stackName string) bool {

	config.Debugf("About to make API calls for failure prediction...")

	// Visit each resource in the template and see if it matches
	// one of our predictions

	// First check to see if the stack already exists.
	// If so, check for possible update issues, and for reasons we can't delete the stack
	// Otherwise, only check for possible create failures
	stack, stackExists := deploy.CheckStack(stackName)

	// TODO: Add all the same params as the `deploy` command has so we can create
	// a change set for updates.

	msg := ""
	if stackExists {
		msg = "exists"
	} else {
		msg = "does not exist"
	}
	config.Debugf("Stack %v %v", stackName, msg)

	numChecked := 0
	numFailed := 0

	// A map of resource type names to prediction functions
	// The function returns (numFailed, numChecked)
	forecasters := make(map[string]func(input PredictionInput) (int, int))

	// If you want to add a prediction for a type that is not already covered, add it here
	// The function must return (numFailed, numChecked)
	// For example:
	// forecasters["AWS::New::Type"] = checkTheNewType

	forecasters["AWS::S3::Bucket"] = checkBucket
	forecasters["AWS::S3::BucketPolicy"] = checkBucketPolicy

	m := source.Map()
	for t, section := range m {
		if t == "Resources" {
			// Iterate over each resource
			for logicalId, resource := range section.(map[string]interface{}) {
				config.Debugf("resource %v %v", logicalId, resource)
				// Iterate over each element in the resource
				for elementName, element := range resource.(map[string]interface{}) {
					config.Debugf("element %v %v", elementName, element)

					// Check the type and call functions that make checks
					// on that type of resource.

					if elementName == "Type" {

						typeName := element.(string) // Should be something like AWS::S3::Bucket

						// Only run the forecaster if it matches the optional --type arg,
						// or if that arg was not provided.
						if ResourceType == "" || ResourceType == typeName {

							input := PredictionInput{}
							input.logicalId = logicalId
							input.source = source
							input.resource = resource
							input.stackName = stackName
							input.stackExists = stackExists
							input.stack = stack

							fmt.Println("Checking", element, logicalId)

							// Call generic prediction functions that we can run against
							// all resources, even if there is not a predictor.

							// Make sure the resource does not already exist
							if cfn.ResourceAlreadyExists(typeName,
								resource.(map[string]interface{}), stackExists) {
								fmt.Printf("%v %v already exists\n", typeName, logicalId)
								numFailed += 1
							}
							numChecked += 1

							// Check permissions
							// (see S3 example, we would need to figure out the arn for each service)
							// TODO - Not sure if this is practical in a generic way

							// See if we have a specific forecaster for this type
							fn, ok := forecasters[typeName]

							if ok {
								// Call the prediction function
								nf, nc := fn(input)
								numFailed += nf
								numChecked += nc
								fmt.Printf("%v %v: %v of %v specific checks failed\n", element, logicalId, nf, nc)
							}
						}
					}
				}
			}
		}
	}

	if numFailed > 0 {
		fmt.Println("Stormy weather ahead!")
		fmt.Println(numFailed, "checks failed out of", numChecked, "total checks")
		return false
	} else {
		fmt.Println("Clear skies! All", numChecked, "checks passed.")
		return true
	}

	// TODO - We might be able to incorporate AWS Config proactive controls here
	// https://aws.amazon.com/blogs/aws/new-aws-config-rules-now-support-proactive-compliance/

	// What about hooks? Could we invoke those handlers to see if they will fail before deployment?

}

// Cmd is the forecast command's entrypoint
var Cmd = &cobra.Command{
	Use:                   "forecast <template> <stackName>",
	Short:                 "Predict deployment failures",
	Long:                  "Outputs warnings about potential deployment failures due to constraints in the account or misconfigurations in the template related to dependencies in the account.",
	Args:                  cobra.ExactArgs(2),
	DisableFlagsInUseLine: true,
	Run: func(cmd *cobra.Command, args []string) {
		fn := args[0]
		stackName := args[1]

		config.Debugf("Generating forecast for %v", fn)

		r, err := os.Open(fn)
		if err != nil {
			panic(ui.Errorf(err, "unable to read '%s'", fn))
		}

		// Read the template
		input, err := io.ReadAll(r)
		if err != nil {
			panic(ui.Errorf(err, "unable to read input"))
		}

		// Parse the template
		source, err := parse.String(string(input))
		if err != nil {
			panic(ui.Errorf(err, "unable to parse input"))
		}

		if !predict(source, stackName) {
			os.Exit(1)
		}

	},
}

func init() {
	Cmd.Flags().BoolVar(&config.Debug, "debug", false, "Output debugging information")
	Cmd.Flags().StringVar(&RoleArn, "role-arn", "", "An optional execution role arn to use for predicting IAM failures")
	// TODO - --op "create", "update", "delete", default: "all"
	Cmd.Flags().StringVar(&ResourceType, "type", "", "Optional resource type to limit checks to only that type")
	Cmd.Flags().StringSliceVar(&Params, "params", []string{}, "set parameter values; use the format key1=value1,key2=value2")
	Cmd.Flags().StringVarP(&ConfigFilePath, "config", "c", "", "YAML or JSON file to set tags and parameters")

}
