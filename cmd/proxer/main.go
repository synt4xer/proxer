package main

import (
	"fmt"
	"io"
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

func main() {
	var db *sqlx.DB

	var rootCmd = &cobra.Command{
		Use:   "proxer",
		Short: "Project Indexer - Local codebase retriever and editor for AI Agents",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			var err error
			db, err = indexer.InitDB()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Failed to initialize DB: %v\n", err)
				os.Exit(1)
			}
		},
		PersistentPostRun: func(cmd *cobra.Command, args []string) {
			if db != nil {
				db.Close()
			}
		},
	}

	var indexCmd = &cobra.Command{
		Use:   "index",
		Short: "Scan and index the local directory",
		Run: func(cmd *cobra.Command, args []string) {
			if err := indexer.IndexProject(db, "."); err != nil {
				fmt.Fprintf(os.Stderr, "Indexing failed: %v\n", err)
				os.Exit(1)
			}
			fmt.Println(`{"status": "indexed_successfully"}`)
		},
	}

	var keyword string
	var searchCmd = &cobra.Command{
		Use:   "search",
		Short: "Search codebase using keyword",
		Run: func(cmd *cobra.Command, args []string) {
			if keyword == "" {
				fmt.Fprintln(os.Stderr, "Search keyword cannot be empty")
				os.Exit(1)
			}

			resultJSON, err := tools.PerformSearch(db, keyword)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(1)
			}
			fmt.Println(resultJSON)
		},
	}
	searchCmd.Flags().StringVarP(&keyword, "keyword", "k", "", "Keyword for FTS5 search (required)")
	searchCmd.MarkFlagRequired("keyword")

	var filepathFlag string
	var startLine, endLine int
	var readCmd = &cobra.Command{
		Use:   "read",
		Short: "Read specific lines from a file",
		Run: func(cmd *cobra.Command, args []string) {
			if filepathFlag == "" {
				fmt.Fprintln(os.Stderr, "Filepath cannot be empty")
				os.Exit(1)
			}

			resultJSON, err := tools.PerformRead(filepathFlag, startLine, endLine)
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(1)
			}
			fmt.Println(resultJSON)
		},
	}
	readCmd.Flags().StringVarP(&filepathFlag, "filepath", "f", "", "Path to the file (required)")
	readCmd.Flags().IntVarP(&startLine, "start", "s", 1, "Start line")
	readCmd.Flags().IntVarP(&endLine, "end", "e", 60, "End line")
	readCmd.MarkFlagRequired("filepath")

	var editFilepath string
	var editStart, editEnd int
	var editCmd = &cobra.Command{
		Use:   "edit",
		Short: "Replace specific lines in a file with new content from STDIN",
		Run: func(cmd *cobra.Command, args []string) {
			if editFilepath == "" {
				fmt.Fprintln(os.Stderr, "Filepath cannot be empty")
				os.Exit(1)
			}
			newContentBytes, err := io.ReadAll(os.Stdin)
			if err != nil {
				fmt.Fprintln(os.Stderr, "Failed to read new content from stdin")
				os.Exit(1)
			}

			resultJSON, err := tools.PerformEdit(db, editFilepath, editStart, editEnd, string(newContentBytes))
			if err != nil {
				fmt.Fprintln(os.Stderr, err.Error())
				os.Exit(1)
			}
			fmt.Println(resultJSON)
		},
	}
	editCmd.Flags().StringVarP(&editFilepath, "filepath", "f", "", "Path to file")
	editCmd.Flags().IntVarP(&editStart, "start", "s", 1, "Start line to replace")
	editCmd.Flags().IntVarP(&editEnd, "end", "e", 1, "End line to replace")
	editCmd.MarkFlagRequired("filepath")

	rootCmd.AddCommand(indexCmd, searchCmd, readCmd, editCmd)

	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "CLI execution error: %v\n", err)
		os.Exit(1)
	}
}
