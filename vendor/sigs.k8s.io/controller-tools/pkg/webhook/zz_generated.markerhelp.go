//go:build !ignore_autogenerated

/*
Copyright2019 The Kubernetes Authors.

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

// Code generated by helpgen. DO NOT EDIT.

package webhook

import (
	"sigs.k8s.io/controller-tools/pkg/markers"
)

func (Config) Help() *markers.DefinitionHelp {
	return &markers.DefinitionHelp{
		Category: "Webhook",
		DetailedHelp: markers.DetailedHelp{
			Summary: "specifies how a webhook should be served. ",
			Details: "It specifies only the details that are intrinsic to the application serving it (e.g. the resources it can handle, or the path it serves on).",
		},
		FieldHelp: map[string]markers.DetailedHelp{
			"Mutating": {
				Summary: "marks this as a mutating webhook (it's validating only if false) ",
				Details: "Mutating webhooks are allowed to change the object in their response, and are called *before* all validating webhooks.  Mutating webhooks may choose to reject an object, similarly to a validating webhook.",
			},
			"FailurePolicy": {
				Summary: "specifies what should happen if the API server cannot reach the webhook. ",
				Details: "It may be either \"ignore\" (to skip the webhook and continue on) or \"fail\" (to reject the object in question).",
			},
			"MatchPolicy": {
				Summary: "defines how the \"rules\" list is used to match incoming requests. Allowed values are \"Exact\" (match only if it exactly matches the specified rule) or \"Equivalent\" (match a request if it modifies a resource listed in rules, even via another API group or version).",
				Details: "",
			},
			"SideEffects": {
				Summary: "specify whether calling the webhook will have side effects. This has an impact on dry runs and `kubectl diff`: if the sideEffect is \"Unknown\" (the default) or \"Some\", then the API server will not call the webhook on a dry-run request and fails instead. If the value is \"None\", then the webhook has no side effects and the API server will call it on dry-run. If the value is \"NoneOnDryRun\", then the webhook is responsible for inspecting the \"dryRun\" property of the AdmissionReview sent in the request, and avoiding side effects if that value is \"true.\"",
				Details: "",
			},
			"TimeoutSeconds": {
				Summary: "allows configuring how long the API server should wait for a webhook to respond before treating the call as a failure. If the timeout expires before the webhook responds, the webhook call will be ignored or the API call will be rejected based on the failure policy. The timeout value must be between 1 and 30 seconds. The timeout for an admission webhook defaults to 10 seconds.",
				Details: "",
			},
			"Groups": {
				Summary: "specifies the API groups that this webhook receives requests for.",
				Details: "",
			},
			"Resources": {
				Summary: "specifies the API resources that this webhook receives requests for.",
				Details: "",
			},
			"Verbs": {
				Summary: "specifies the Kubernetes API verbs that this webhook receives requests for. ",
				Details: "Only modification-like verbs may be specified. May be \"create\", \"update\", \"delete\", \"connect\", or \"*\" (for all).",
			},
			"Versions": {
				Summary: "specifies the API versions that this webhook receives requests for.",
				Details: "",
			},
			"Name": {
				Summary: "indicates the name of this webhook configuration. Should be a domain with at least three segments separated by dots",
				Details: "",
			},
			"Path": {
				Summary: "specifies that path that the API server should connect to this webhook on. Must be prefixed with a '/validate-' or '/mutate-' depending on the type, and followed by $GROUP-$VERSION-$KIND where all values are lower-cased and the periods in the group are substituted for hyphens. For example, a validating webhook path for type batch.tutorial.kubebuilder.io/v1,Kind=CronJob would be /validate-batch-tutorial-kubebuilder-io-v1-cronjob",
				Details: "",
			},
			"WebhookVersions": {
				Summary: "specifies the target API versions of the {Mutating,Validating}WebhookConfiguration objects itself to generate. The only supported value is v1. Defaults to v1.",
				Details: "",
			},
			"AdmissionReviewVersions": {
				Summary: "is an ordered list of preferred `AdmissionReview` versions the Webhook expects.",
				Details: "",
			},
			"ReinvocationPolicy": {
				Summary: "allows mutating webhooks to request reinvocation after other mutations ",
				Details: "To allow mutating admission plugins to observe changes made by other plugins, built-in mutating admission plugins are re-run if a mutating webhook modifies an object, and mutating webhooks can specify a reinvocationPolicy to control whether they are reinvoked as well.",
			},
		},
	}
}

func (Generator) Help() *markers.DefinitionHelp {
	return &markers.DefinitionHelp{
		Category: "",
		DetailedHelp: markers.DetailedHelp{
			Summary: "generates (partial) {Mutating,Validating}WebhookConfiguration objects.",
			Details: "",
		},
		FieldHelp: map[string]markers.DetailedHelp{
			"HeaderFile": {
				Summary: "specifies the header text (e.g. license) to prepend to generated files.",
				Details: "",
			},
			"Year": {
				Summary: "specifies the year to substitute for \" YEAR\" in the header file.",
				Details: "",
			},
		},
	}
}