package main

import (
	"log"

	"github.com/mobingilabs/mobingi-sdk-go/pkg/cmdline"
	"github.com/mobingilabs/mobingi-sdk-go/pkg/debug"
	"github.com/spf13/cobra"
)

var (
	// main parent (root) command
	rootCmd = &cobra.Command{
		Use:   "icariumd",
		Short: "pullr docker auto-builder",
		Long:  `Docker auto-builder service for pullr.`,
		Run:   run,
	}
)

func run(cmd *cobra.Command, args []string) {
	debug.Info("main")
}

func main() {
	log.SetFlags(0)
	pfx := "[" + cmdline.Args0() + "]: "
	log.SetPrefix(pfx)

	if err := rootCmd.Execute(); err != nil {
		log.Fatalln(err)
	}
}
