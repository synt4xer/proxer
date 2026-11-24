package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/jmoiron/sqlx"
	"github.com/spf13/cobra"
	"github.com/synt4xer/proxer/database"
	"github.com/synt4xer/proxer/internal/config"
	"github.com/synt4xer/proxer/internal/domain"
	"github.com/synt4xer/proxer/internal/indexer"
	"github.com/synt4xer/proxer/internal/tools"
)

const manageDBAnnotation = "ownDB"

type cliState struct {
	root        string
	dbPath      string
	installMode string
	skillDir    string
	cfg         config.ProjectConfig
	db          *sqlx.DB
	readOnly    bool
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	state := &cliState{}
	defer func() {
		if state.db != nil {
			state.db.Close()
		}
	}()

	rootCmd := newRootCommand(state)
	rootCmd.SetArgs(args)
	rootCmd.SetIn(stdin)
	rootCmd.SetOut(stdout)
	rootCmd.SetErr(stderr)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func newRootCommand(state *cliState) *cobra.Command {
	rootCmd := &cobra.Command{
		Use:     "proxer",
		Short:   "Project Indexer - Local codebase retriever and editor for AI Agents",
		Version: config.Version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			rootFlag, err := cmd.Flags().GetString("root")
			if err != nil {
				return fmt.Errorf("failed to read the root flag: %w", err)
			}

			state.root, err = config.ResolveRoot(rootFlag)
			if err != nil {
				return fmt.Errorf("failed to resolve the project root: %w", err)
			}

			state.cfg, err = config.LoadProjectConfig(state.root)
			if err != nil {
				return fmt.Errorf("failed to load the project config: %w", err)
			}

			dbFlag, err := cmd.Flags().GetString("db")
			if err != nil {
				return fmt.Errorf("failed to read the db flag: %w", err)
			}

			loc, err := config.ResolveDBPath(state.root, dbFlag, cmd.Name() == "mcp")
			if err != nil {
				return fmt.Errorf("failed to resolve database path: %w", err)
			}
			state.dbPath = loc.DBPath
			state.installMode = string(loc.Mode)
			state.skillDir = loc.SkillDir
			if cmd.Annotations[manageDBAnnotation] == "true" {
				return nil
			}

			state.db, err = database.Open(state.dbPath, database.OpenOptions{
				Chunking:       indexer.Chunking(),
				IndexSensitive: state.cfg.IndexSensitive,
			})
			if err != nil {
				if database.IsReadOnlyError(err) && (cmd.Name() == "search" || cmd.Name() == "status" || cmd.Name() == "doctor") && database.IsReadableFile(state.dbPath) {
					roDB, roErr := database.OpenReadOnly(state.dbPath, indexer.Chunking())
					if roErr == nil {
						state.db = roDB
						state.readOnly = true
						return nil
					}
				}
				return fmt.Errorf("failed to initialize DB: %w", err)
			}
			return nil
		},
	}
	rootCmd.PersistentFlags().String("root", "", "Project root to index and search (default: $"+config.RootEnvVar+", then the current directory)")
	rootCmd.PersistentFlags().String("db", "", "Explicit path to the index database (default: $"+config.DBEnvVar+", then install-mode routing)")

	indexCmd := &cobra.Command{
		Use:   "index",
		Short: "Scan and index the local directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			refreshed, err := indexer.IndexProjectWithOptions(state.db, state.root, indexer.IndexOptions{
				IgnoredDirs:    state.cfg.IgnoredDirs,
				IndexSensitive: state.cfg.IndexSensitive,
			})
			if err != nil {
				return fmt.Errorf("indexing failed: %w", err)
			}
			hadChunkChanges := refreshed.Added > 0 || refreshed.Changed > 0 || refreshed.Removed > 0
			maint, err := database.Maintain(state.db, state.dbPath, database.MaintainOptions{
				HadDiff: hadChunkChanges,
			})
			if err != nil {
				return fmt.Errorf("indexing succeeded, but maintenance failed: %w", err)
			}
			var dbSize int64
			if info, statErr := os.Stat(state.dbPath); statErr == nil {
				dbSize = info.Size()
			}
			return writeJSON(cmd.OutOrStdout(), domain.IndexResponse{
				Status:         "indexed_successfully",
				Refreshed:      refreshed.ToSummary(),
				DBSizeBytes:    dbSize,
				IndexSensitive: state.cfg.IndexSensitive,
				PurgePending:   maint.PurgePending,
				Warning:        maint.Warning,
			})
		},
	}

	var keyword string
	var limit int
	var kind string
	var noRefresh bool
	searchCmd := &cobra.Command{
		Use:   "search",
		Short: "Search codebase using keyword",
		RunE: func(cmd *cobra.Command, args []string) error {
			if keyword == "" {
				return errors.New("search keyword cannot be empty")
			}

			result, err := tools.PerformSearch(state.db, state.root, keyword, tools.SearchOptions{
				Ctx:            cmd.Context(),
				NoRefresh:      noRefresh || config.RefreshDisabled(),
				ReadOnly:       state.readOnly,
				IgnoredDirs:    state.cfg.IgnoredDirs,
				IndexSensitive: state.cfg.IndexSensitive,
				Limit:          limit,
				Kind:           kind,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	searchCmd.Flags().StringVarP(&keyword, "keyword", "k", "", "Keyword for FTS5 search (required)")
	searchCmd.Flags().IntVarP(&limit, "limit", "n", 5, "Maximum number of results to return (1 to 50)")
	searchCmd.Flags().StringVar(&kind, "kind", "all", "Which files to search: code, docs, or all (all orders code before docs before generated assets)")
	searchCmd.Flags().BoolVar(&noRefresh, "no-refresh", false, "Query the index as it stands instead of bringing it up to date first (also PROXER_NO_REFRESH=1)")
	searchCmd.MarkFlagRequired("keyword")

	var filepathFlag string
	var startLine, endLine int
	var numbered bool
	readCmd := &cobra.Command{
		Use:   "read",
		Short: "Read specific lines from a file",
		Annotations: map[string]string{
			manageDBAnnotation: "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if filepathFlag == "" {
				return errors.New("filepath cannot be empty")
			}

			result, err := tools.PerformReadWithOptions(state.root, filepathFlag, startLine, endLine, tools.ReadOptions{
				Numbered: numbered,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	readCmd.Flags().StringVarP(&filepathFlag, "filepath", "f", "", "Path to the file (required)")
	readCmd.Flags().IntVarP(&startLine, "start", "s", 1, "Start line")
	readCmd.Flags().IntVarP(&endLine, "end", "e", 60, "End line")
	readCmd.Flags().BoolVarP(&numbered, "numbered", "n", false, "Prefix each line with its 1-based line number and a tab")
	readCmd.MarkFlagRequired("filepath")

	var editFilepath string
	var editStart, editEnd int
	var editHash string
	var editOld string
	var editOccurrence int
	editCmd := &cobra.Command{
		Use:   "edit",
		Short: "Replace specific lines or an anchor block in a file with new content from STDIN",
		RunE: func(cmd *cobra.Command, args []string) error {
			if editFilepath == "" {
				return errors.New("filepath cannot be empty")
			}
			newContentBytes, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return fmt.Errorf("failed to read new content from stdin: %w", err)
			}

			hasRange := cmd.Flags().Changed("start") || cmd.Flags().Changed("end")
			result, err := tools.PerformEditWithOptions(cmd.Context(), state.db, state.root, editFilepath, editStart, editEnd, string(newContentBytes), editHash, tools.EditOptions{
				IgnoredDirs:    state.cfg.IgnoredDirs,
				IndexSensitive: state.cfg.IndexSensitive,
				OldContent:     editOld,
				Occurrence:     editOccurrence,
				HasRange:       hasRange,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	editCmd.Flags().StringVarP(&editFilepath, "filepath", "f", "", "Path to file")
	editCmd.Flags().IntVarP(&editStart, "start", "s", 1, "Start line to replace")
	editCmd.Flags().IntVarP(&editEnd, "end", "e", 1, "End line to replace")
	editCmd.Flags().StringVar(&editOld, "old", "", "Exact text block to replace (anchor mode; mutually exclusive with --start/--end)")
	editCmd.Flags().IntVar(&editOccurrence, "occurrence", 0, "1-based occurrence index when --old matches multiple times (default 0 requires a unique match)")
	editCmd.MarkFlagRequired("filepath")
	editCmd.Flags().StringVar(&editHash, "hash", "", "Hash returned by search or read; the edit is rejected if the file changed since then")

	var statusVacuum bool
	var statusPrune bool
	statusCmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"doctor"},
		Short:   "Inspect index metadata, size, permissions, and maintenance state",
		RunE: func(cmd *cobra.Command, args []string) error {
			result, err := tools.PerformStatus(state.db, state.root, tools.StatusOptions{
				DBPath:         state.dbPath,
				IndexSensitive: state.cfg.IndexSensitive,
				ReadOnly:       state.readOnly,
				Vacuum:         statusVacuum,
				Prune:          statusPrune,
				InstallMode:    state.installMode,
				SkillDir:       state.skillDir,
			})
			if err != nil {
				return err
			}
			return writeJSON(cmd.OutOrStdout(), result)
		},
	}
	statusCmd.Flags().BoolVar(&statusVacuum, "vacuum", false, "Run FTS5 optimize, VACUUM, and WAL checkpoint on the database")
	statusCmd.Flags().BoolVar(&statusPrune, "prune", false, "Delete orphaned harness databases whose recorded project root no longer exists")

	rootCmd.AddCommand(indexCmd, searchCmd, readCmd, editCmd, statusCmd, newMCPCommand(state))
	rootCmd.SilenceUsage = true
	rootCmd.SilenceErrors = true

	return rootCmd
}

func writeJSON(w io.Writer, v any) error {
	out, err := tools.EncodeJSON(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}
	_, err = fmt.Fprintln(w, out)
	return err
}
