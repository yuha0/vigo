package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yuha0/vigo/internal/secretbox"
)

// parseToken accepts a raw token or a JSON object with a "token" field (and
// optionally "name", the client name it was paired under), as written by
// the reference pair.py.
func parseToken(b []byte) (token, name string, err error) {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "{") {
		var f struct {
			Token string `json:"token"`
			Name  string `json:"name"`
		}
		if err := json.Unmarshal([]byte(s), &f); err != nil {
			return "", "", errors.New("the token file is not valid JSON")
		}
		s, name = strings.TrimSpace(f.Token), f.Name
	}
	if s == "" || strings.ContainsAny(s, " \t\r\n\"") {
		return "", "", errors.New("the token file holds no usable token")
	}
	return s, name, nil
}

func importTokenCommand(o *options) *cobra.Command {
	var file, clientName string
	cmd := &cobra.Command{
		Use:   "import-token --file PATH",
		Short: "Store an existing TV token so vigo-core needs no re-pairing",
		Long: "Reads a raw token, or a JSON file with a \"token\" field, and stores it encrypted.\n" +
			"The client name defaults to the file's \"name\" field, else \"vigo\"; it must match the name the token was paired under.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			token, name, err := parseToken(b)
			if err != nil {
				return err
			}
			if clientName == "" {
				clientName = name
			}
			if clientName == "" {
				clientName = "vigo"
			}
			ctx := cmd.Context()
			d, err := openDB(ctx, o.db)
			if err != nil {
				return err
			}
			defer d.Close()
			if _, err := d.Migrate(ctx); err != nil {
				return err
			}
			if err := d.TV().SetToken(ctx, token, clientName); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Stored the TV token for client name %q. A running leader picks it up after a restart.\n", clientName)
			return nil
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "token file")
	cmd.Flags().StringVar(&clientName, "client-name", "", "client name the token was paired under")
	cmd.MarkFlagRequired("file")
	return cmd
}

func ackModelCommand(o *options) *cobra.Command {
	return &cobra.Command{
		Use:   "ack-model",
		Short: "Acknowledge the TV's current model string, clearing the model_changed warning",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := openDB(cmd.Context(), o.db)
			if err != nil {
				return err
			}
			defer d.Close()
			n, err := d.AcknowledgeModels(cmd.Context())
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Acknowledged %d model(s).\n", n)
			return nil
		},
	}
}

func keyringCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "keyring",
		Short: "Create or rotate the encryption keyring",
	}
	var newOut, newID string
	create := &cobra.Command{
		Use:   "new --out PATH",
		Short: "Write a new keyring with one random primary key",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			b, err := secretbox.AddPrimary(nil, newID)
			if err != nil {
				return err
			}
			return writeNew(newOut, b)
		},
	}
	create.Flags().StringVar(&newOut, "out", "", "file to create")
	create.Flags().StringVar(&newID, "id", "k1", "key id")
	create.MarkFlagRequired("out")

	var rotFile, rotID string
	rotate := &cobra.Command{
		Use:   "rotate --file PATH --id ID",
		Short: "Add a random key to a keyring and make it primary; old keys stay for decryption",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			old, err := os.ReadFile(rotFile)
			if err != nil {
				return err
			}
			b, err := secretbox.AddPrimary(old, rotID)
			if err != nil {
				return err
			}
			return os.WriteFile(rotFile, append(b, '\n'), 0o600)
		},
	}
	rotate.Flags().StringVar(&rotFile, "file", "", "keyring file to update")
	rotate.Flags().StringVar(&rotID, "id", "", "id of the new key")
	rotate.MarkFlagRequired("file")
	rotate.MarkFlagRequired("id")

	cmd.AddCommand(create, rotate)
	return cmd
}

func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
