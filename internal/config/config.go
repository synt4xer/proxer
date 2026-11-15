package config

const (
	DBFile      = ".proxer.db"
	MaxFileSize = 1024 * 1024 // 1 MB
	ChunkSize   = 60
	OverlapSize = 15
)

var IgnoredDirs = []string{
	".git", ".vscode", ".idea", ".github",
	"node_modules", "vendor",
	"dist", "build", "out", "target",
	"bin", "obj", "tmp", "coverage",
	".next", ".nuxt", ".svelte-kit",
	".astro",
}

var AllowedExtensions = []string{
	".go", ".rb", ".py", ".php", ".rs", ".java", ".c", ".cpp", ".cs",
	".js", ".jsx", ".ts", ".tsx", ".vue", ".svelte", ".astro",
	".html", ".css", ".scss", ".sass", ".less", ".tailwind.css",
	".md", ".json", ".yaml", ".yml", ".toml", ".ini", ".env",
	".sql", ".sh", ".bash",
}

var AllowedExactNames = []string{
	"Dockerfile", "Makefile", "docker-compose.yml", "Gemfile", "package.json", "go.mod", "go.sum",
}
