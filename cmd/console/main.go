package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"

	"github.com/checkmarxDev/ice/internal/storage"
	"github.com/checkmarxDev/ice/internal/tracker"
	"github.com/checkmarxDev/ice/pkg/engine"
	"github.com/checkmarxDev/ice/pkg/engine/query"
	"github.com/checkmarxDev/ice/pkg/ice"
	"github.com/checkmarxDev/ice/pkg/model"
	"github.com/checkmarxDev/ice/pkg/parser"
	jsonParser "github.com/checkmarxDev/ice/pkg/parser/json"
	terraformParser "github.com/checkmarxDev/ice/pkg/parser/terraform"
	yamlParser "github.com/checkmarxDev/ice/pkg/parser/yaml"
	"github.com/checkmarxDev/ice/pkg/source"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const scanID = "console"

func main() { // nolint:funlen,gocyclo
	var (
		path        string
		queryPath   string
		outputPath  string
		payloadPath string
		verbose     bool
	)

	ctx := context.Background()
	if verbose {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
	}
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	rootCmd := &cobra.Command{
		Use:   "iacScanner",
		Short: "Security inspect tool for Infrastructure as Code files",
		RunE: func(cmd *cobra.Command, args []string) error {
			store := storage.NewMemoryStorage()
			if verbose {
				log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stdout})
			} else {
				log.Logger = log.Output(zerolog.ConsoleWriter{Out: ioutil.Discard})
			}

			querySource := &query.FilesystemSource{
				Source: queryPath,
			}

			t := &tracker.CITracker{}
			inspector, err := engine.NewInspector(ctx, querySource, engine.DefaultVulnerabilityBuilder, t)
			if err != nil {
				return err
			}

			var excludeFiles []string
			if payloadPath != "" {
				excludeFiles = append(excludeFiles, payloadPath)
			}

			filesSource, err := source.NewFileSystemSourceProvider(path, excludeFiles)
			if err != nil {
				return err
			}

			combinedParser := parser.NewBuilder().
				Add(&jsonParser.Parser{}).
				Add(&yamlParser.Parser{}).
				Add(terraformParser.NewDefault()).
				Build()

			service := &ice.Service{
				SourceProvider: filesSource,
				Storage:        store,
				Parser:         combinedParser,
				Inspector:      inspector,
				Tracker:        t,
			}

			if scanErr := service.StartScan(ctx, scanID); scanErr != nil {
				return scanErr
			}

			result, err := store.GetVulnerabilities(ctx, scanID)
			if err != nil {
				return err
			}

			files, err := store.GetFiles(ctx, scanID)
			if err != nil {
				return err
			}

			counters := model.Counters{
				ScannedFiles:           t.FoundFiles,
				FailedToScanFiles:      t.FoundFiles - t.ParsedFiles,
				TotalQueries:           t.LoadedQueries,
				FailedToExecuteQueries: t.LoadedQueries - t.ExecutedQueries,
			}

			summary := model.CreateSummary(counters, result)

			if payloadPath != "" {
				if err := printToJSONFile(payloadPath, files.Combine()); err != nil {
					return err
				}
			}

			if outputPath != "" {
				if err := printToJSONFile(outputPath, summary); err != nil {
					return err
				}
			}

			if err := printResult(summary); err != nil {
				return err
			}

			if len(summary.FailedQueries) > 0 {
				os.Exit(1)
			}

			return nil
		},
	}

	rootCmd.Flags().StringVarP(&path, "path", "p", "", "path to file or directory to scan")
	rootCmd.Flags().StringVarP(&queryPath, "queries-path", "q", "./assets/queries", "path to directory with queries")
	rootCmd.Flags().StringVarP(&outputPath, "output-path", "o", "", "file path to store result in json format")
	rootCmd.Flags().StringVarP(&payloadPath, "payload-path", "d", "", "file path to store source internal representation in JSON format")
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "verbose scan")
	if err := rootCmd.MarkFlagRequired("path"); err != nil {
		log.Err(err).Msg("failed to add command required flags")
	}

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		os.Exit(-1)
	}
}

func printResult(summary model.Summary) error {
	fmt.Printf("Files scanned: %d\n", summary.ScannedFiles)
	fmt.Printf("Files failed to scan: %d\n", summary.FailedToScanFiles)
	fmt.Printf("Queries loaded: %d\n", summary.TotalQueries)
	fmt.Printf("Queries failed to execute: %d\n", summary.FailedToExecuteQueries)
	for _, q := range summary.FailedQueries {
		fmt.Printf("%s, Severity: %s, Results: %d\n", q.QueryName, q.Severity, len(q.Files))
		for _, f := range q.Files {
			fmt.Printf("\t%s:%d\n", f.FileName, f.Line)
		}
	}

	return nil
}

func printToJSONFile(path string, body interface{}) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.ModePerm)
	if err != nil {
		return err
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Err(err).Msgf("failed to close file %s", path)
		}

		log.Info().Str("fileName", path).Msgf("Results saved to file %s", path)
	}()

	encoder := json.NewEncoder(f)
	encoder.SetIndent("", "\t")

	return encoder.Encode(body)
}
