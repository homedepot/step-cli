package ssh

import (
	"net/http"

	"github.com/pkg/errors"
	"github.com/smallstep/certificates/api"
	"github.com/smallstep/certificates/authority/provisioner"
	"github.com/smallstep/certificates/ca"
	"github.com/smallstep/cli/command"
	"github.com/smallstep/cli/crypto/keys"
	"github.com/smallstep/cli/crypto/sshutil"
	"github.com/smallstep/cli/token"
	"github.com/smallstep/cli/ui"
	"github.com/smallstep/cli/utils/cautils"
	"github.com/urfave/cli"
	"golang.org/x/crypto/ssh"
)

// init creates and registers the ssh command
func init() {
	cmd := cli.Command{
		Name:      "ssh",
		Usage:     "create and manage ssh certificates",
		UsageText: "step ssh <subcommand> [arguments] [global-flags] [subcommand-flags]",
		Description: `**step ssh** command group provides facilities to sign SSH certificates.

## EXAMPLES

Generate a new SSH key pair and user certificate:
'''
$ step ssh certificate mariano@work id_ecdsa
'''

Generate a new SSH key pair and host certificate:
'''
$ step ssh certificate --host internal.example.com ssh_host_ecdsa_key
'''`,
		Subcommands: cli.Commands{
			certificateCommand(),
			configCommand(),
			loginCommand(),
			logoutCommand(),
			inspectCommand(),
			listCommand(),
			fingerPrintCommand(),
			// proxyCommand(),
			proxycommandCommand(),
			checkHostCommand(),
			hostsCommand(),
			renewCommand(),
			revokeCommand(),
			rekeyCommand(),
		},
	}

	command.Register(cmd)
}

var (
	sshPrincipalFlag = cli.StringSliceFlag{
		Name: "principal,n",
		Usage: `Add the principals (users or hosts) that the token is authorized to
		request. The signing request using this token won't be able to add
		extra names. Use the '--principal' flag multiple times to configure
		multiple ones. The '--principal' flag and the '--token' flag are
		mutually exlusive.`,
	}

	sshHostFlag = cli.BoolFlag{
		Name:  "host",
		Usage: `Create a host certificate instead of a user certificate.`,
	}

	sshSignFlag = cli.BoolFlag{
		Name:  "sign",
		Usage: `Sign the public key passed as an argument instead of creating one.`,
	}

	sshPasswordFileFlag = cli.StringFlag{
		Name:  "password-file",
		Usage: `The path to the <file> containing the password to encrypt the private key.`,
	}

	sshProvisionerPasswordFlag = cli.StringFlag{
		Name: "provisioner-password-file",
		Usage: `The path to the <file> containing the password to decrypt the one-time token
		generating key.`,
	}

	sshAddUserFlag = cli.BoolFlag{
		Name:  "add-user",
		Usage: `Create a user provisioner certificate used to create a new user.`,
	}

	sshPrivateKeyFlag = cli.StringFlag{
		Name: "private-key",
		Usage: `When signing an existing public key, use this flag to specify the corresponding
private key so that the pair can be added to an SSH Agent.`,
	}

	sshConnectFlag = cli.StringFlag{
		Name:  "connect,c",
		Usage: "Remote <host:port> to connect to.",
	}

	sshBastionFlag = cli.StringFlag{
		Name:  "via,bastion",
		Usage: "The <server> to use to proxy an ssh connection.",
	}

	sshBastionCommandFlag = cli.StringFlag{
		Name:  "via-command,bastion-command",
		Usage: "The <command> to proxy the connection through a bastion server. Defaults to `nc -q0 %h %p`.",
		Value: "nc -q0 %h %p",
	}

	sshProxyFlag = cli.StringFlag{
		Name:  "proxy,p",
		Usage: "The <command> to use to connect to the server.",
	}
)

func loginOnUnauthorized(ctx *cli.Context) (ca.RetryFunc, error) {
	flow, err := cautils.NewCertificateFlow(ctx)
	if err != nil {
		return nil, err
	}

	client, err := cautils.NewClient(ctx)
	if err != nil {
		return nil, err
	}

	return func(code int) bool {
		if code != http.StatusUnauthorized {
			return false
		}

		fail := func(err error) bool {
			ui.Printf("{{ \"%v\" | red }}\n", err)
			return false
		}

		// Generate OIDC token
		tok, err := flow.GenerateIdentityToken(ctx)
		if err != nil {
			return fail(err)
		}
		jwt, err := token.ParseInsecure(tok)
		if err != nil {
			return fail(err)
		}
		if jwt.Payload.Email == "" {
			return fail(errors.New("error creating identity token: email address cannot be empty"))
		}

		// Generate SSH Keys
		pub, priv, err := keys.GenerateDefaultKeyPair()
		if err != nil {
			return fail(err)
		}
		sshPub, err := ssh.NewPublicKey(pub)
		if err != nil {
			return fail(err)
		}

		// Generate identity CSR (x509)
		identityCSR, identityKey, err := ca.NewIdentityRequest(jwt.Payload.Email)
		if err != nil {
			return fail(err)
		}

		resp, err := client.SSHSign(&api.SSHSignRequest{
			PublicKey:   sshPub.Marshal(),
			OTT:         tok,
			CertType:    provisioner.SSHUserCert,
			IdentityCSR: *identityCSR,
		})
		if err != nil {
			return fail(err)
		}

		// Write x509 identity certificate
		if err := ca.WriteDefaultIdentity(resp.IdentityCertificate, identityKey); err != nil {
			return fail(err)
		}

		// Add ssh certificate to the agent, ignore errors.
		if agent, err := sshutil.DialAgent(); err == nil {
			agent.AddCertificate(jwt.Payload.Email, resp.Certificate.Certificate, priv)
		}

		return true
	}, nil
}

// tokenHasEmail returns if the token payload has an email address. This is
// mainly used on OIDC token.
func tokenHasEmail(s string) (string, bool) {
	jwt, err := token.ParseInsecure(s)
	if err != nil {
		return "", false
	}
	return jwt.Payload.Email, jwt.Payload.Email != ""
}
