// SPDX-License-Identifier: MPL-2.0

package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/ignitify-dev/terraform-provider-ignitify/internal/provider"
)

// version is set at build time (-ldflags "-X main.version=...").
var version = "dev"

const registryAddress = "registry.terraform.io/ignitify-dev/ignitify"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run the provider with support for debuggers such as delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: registryAddress,
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
