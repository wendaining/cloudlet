package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/wendaining/cloudlet/internal/apigen"
	"github.com/wendaining/cloudlet/pkg/client"
)

func newProjectsCommand() *cobra.Command {
	var endpoint string
	var cookie string
	var output string
	projects := &cobra.Command{Use: "projects", Short: "List and inspect accessible projects", Args: cobra.NoArgs}
	projects.AddCommand(&cobra.Command{
		Use: "list", Short: "List accessible projects", Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			api, err := projectClient(endpoint, cookie)
			if err != nil {
				return err
			}
			items, err := api.ListProjects(cmd.Context())
			if err != nil {
				return err
			}
			return writeProjects(cmd.OutOrStdout(), items, output)
		},
	})
	projects.AddCommand(&cobra.Command{
		Use: "get <project-id>", Short: "Show an accessible project", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			api, err := projectClient(endpoint, cookie)
			if err != nil {
				return err
			}
			item, err := api.GetProject(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			return writeProject(cmd.OutOrStdout(), item, output)
		},
	})
	projects.PersistentFlags().StringVar(&endpoint, "endpoint", "http://127.0.0.1:8080", "management API endpoint")
	projects.PersistentFlags().StringVar(&cookie, "cookie", os.Getenv("CLOUDLET_COOKIE"), "authenticated session Cookie header (or CLOUDLET_COOKIE)")
	projects.PersistentFlags().StringVar(&output, "output", "table", "output format: table or json")
	return projects
}

func projectClient(endpoint, cookie string) (*client.Client, error) {
	api, err := client.New(endpoint)
	if err != nil {
		return nil, err
	}
	if err := api.SetSessionCookie(cookie); err != nil {
		return nil, err
	}
	return api, nil
}

func writeProjects(w io.Writer, projects []apigen.Project, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(projects)
	}
	if format != "table" {
		return fmt.Errorf("unsupported output format %q (choose table or json)", format)
	}
	table := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "ID\tNAME"); err != nil {
		return err
	}
	for _, project := range projects {
		if _, err := fmt.Fprintf(table, "%s\t%s\n", project.Id, project.Name); err != nil {
			return err
		}
	}
	return table.Flush()
}

func writeProject(w io.Writer, project apigen.Project, format string) error {
	if format == "json" {
		return json.NewEncoder(w).Encode(project)
	}
	if format != "table" {
		return fmt.Errorf("unsupported output format %q (choose table or json)", format)
	}
	_, err := fmt.Fprintf(w, "ID\t%s\nNAME\t%s\n", project.Id, strings.TrimSpace(project.Name))
	return err
}
