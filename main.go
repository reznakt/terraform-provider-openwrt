package main

import (
	"context"
	"flag"
	"log"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"

	"github.com/reznakt/terraform-provider-openwrt/internal/provider"
)

// version is set by the build (-X main.version=...).
var version = "dev"

func main() {
	var debug bool
	flag.BoolVar(&debug, "debug", false, "run with support for debuggers like delve")
	flag.Parse()

	err := providerserver.Serve(context.Background(), provider.New(version), providerserver.ServeOpts{
		Address: "registry.opentofu.org/reznakt/openwrt",
		Debug:   debug,
	})
	if err != nil {
		log.Fatal(err.Error())
	}
}
