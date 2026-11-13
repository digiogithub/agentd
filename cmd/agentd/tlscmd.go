package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/digiogithub/agentd/internal/config"
	"github.com/digiogithub/agentd/internal/tlsutil"
)

// tlsCmd is `agentd serve tls [show|rotate]`.
func tlsCmd() *cobra.Command {
	var cfgPath string
	load := func() (config.APIConfig, error) {
		var cfg *config.Config
		var err error
		if cfgPath != "" {
			cfg, err = config.Load(cfgPath)
		} else {
			cfg, err = config.LoadDefault()
		}
		if err != nil {
			return config.APIConfig{}, err
		}
		return cfg.API, nil
	}
	cmd := &cobra.Command{
		Use:   "tls",
		Short: "Show or rotate the gateway TLS certificate and its pin",
	}
	show := &cobra.Command{
		Use:   "show",
		Short: "Print the TLS mode, SPKI pin, SANs and expiry (generates the self-signed cert if missing)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := load()
			if err != nil {
				return err
			}
			return tlsShow(cmd.OutOrStdout(), api, "")
		},
	}
	rotate := &cobra.Command{
		Use:   "rotate",
		Short: "Generate a new self-signed key/certificate (new pin; restart `agentd serve`, re-pair devices)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := load()
			if err != nil {
				return err
			}
			if err := tlsRotate(api, ""); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "rotated: restart `agentd serve`; paired devices must re-pair or confirm the new pin")
			return tlsShow(cmd.OutOrStdout(), api, "")
		},
	}
	cmd.PersistentFlags().StringVar(&cfgPath, "config", "", "config file (default: the usual config.toml)")
	cmd.AddCommand(show, rotate)
	cmd.RunE = show.RunE
	cmd.Args = cobra.NoArgs
	return cmd
}

func tlsRotate(api config.APIConfig, dir string) error {
	if api.TLS != config.APITLSSelfSigned {
		return fmt.Errorf("api.tls = %q: only the self-signed certificate can be rotated", api.TLS)
	}
	cfg, err := tlsConfigFor(api, dir)
	if err != nil {
		return err
	}
	_, _, err = tlsutil.Generate(cfg)
	return err
}

func tlsShow(w io.Writer, api config.APIConfig, dir string) error {
	if api.TLS == config.APITLSOff {
		fmt.Fprintln(w, "tls: off (loopback only; no certificate)")
		return nil
	}
	cfg, err := tlsConfigFor(api, dir)
	if err != nil {
		return err
	}
	src, err := tlsutil.Open(api.TLS, cfg)
	if err != nil {
		return err
	}
	in := src.Info()
	fmt.Fprintf(w, "mode:    %s\npin:     %s\n", api.TLS, in.Pin)
	fmt.Fprintf(w, "file:    %s\n", in.Path)
	fmt.Fprintf(w, "names:   %s\nips:     %s\n", strings.Join(in.DNSNames, ", "), strings.Join(in.IPs, ", "))
	fmt.Fprintf(w, "expires: %s\n", in.NotAfter.UTC().Format(time.RFC3339))
	return nil
}
