package cmd

import (
	"log"

	"github.com/alexellis/inlets/pkg/server"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func init() {
	inletsCmd.AddCommand(serverCmd)
	serverCmd.Flags().IntP("port", "p", 8000, "port for server")
	serverCmd.Flags().StringP("token", "t", "", "token for authentication")
	serverCmd.Flags().Bool("print-token", true, "prints the token in server mode")
	serverCmd.Flags().String("gateway-timeout", "5s", "timeout for upstream gateway")
}

// serverCmd represents the server sub command.
var serverCmd = &cobra.Command{
	Use:   "server",
	Short: "Start the tunnel server on a machine with a publicly-accessible IPv4 IP address such as a VPS.",
	Long: `Start the tunnel server on a machine with a publicly-accessible IPv4 IP address such as a VPS.

Example: inlets server -p 80 
Note: You can pass the --token argument followed by a token value to both the server and client to prevent unauthorized connections to the tunnel.`,
	RunE: runServer,
}

// runServer does the actual work of reading the arguments passed to the server sub command.
func runServer(cmd *cobra.Command, _ []string) error {
	token, err := cmd.Flags().GetString("token")
	if err != nil {
		return errors.Wrap(err, "failed to get 'token' value.")
	}

	printToken, err := cmd.Flags().GetBool("print-token")
	if err != nil {
		return errors.Wrap(err, "failed to get 'print-token' value.")
	}

	if len(token) > 0 && printToken {
		log.Printf("Server token: %s", token)
	}

	gatewayTimeoutRaw, err := cmd.Flags().GetString("gateway-timeout")
	if err != nil {
		return errors.Wrap(err, "failed to get the 'gateway-timeout' value.")
	}

	gatewayTimeout, gatewayTimeoutErr := time.ParseDuration(gatewayTimeoutRaw)
	if gatewayTimeoutErr != nil {
		return gatewayTimeoutErr
	}
	log.Printf("Gateway timeout: %f secs\n", gatewayTimeout.Seconds())

	addr, err := cmd.Flags().GetString("addr")
	if err != nil {
		return errors.Wrap(err, "failed to get the 'port' value.")
	}

	cfg := &server.Configuration{
		Addr:           addr,
		GatewayTimeout: gatewayTimeout,
		Token:          token,
	}

	inletsServer := server.New(cfg)

	inletsServer.Serve()
	return nil
}
