package pkgdoc

import (
	"go/doc"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// pathBannerPattern 匹配"整行只有一个文件路径"的注释横幅。
//
// 这类横幅没有任何信息量，却因为紧挨 package 条款而成为包文档：
// go doc 显示的第一行就成了那条路径，而不是真正的包说明。
var pathBannerPattern = regexp.MustCompile("^[A-Za-z0-9_./-]+\\.(go|sql)$")

// Test全仓包文档合规
//
// 遍历仓库里的每个 Go 包与每个源码文件，逐项检查上面列出的规则。
// 用子测试按包/文件分组，失败信息能直接指到出问题的那个位置。
func Test全仓包文档合规(t *testing.T) {
	root := repoRoot(t)

	t.Run("Go 包文档", func(t *testing.T) {
		for _, dir := range goPackageDirs(t, root) {
			t.Run(relativePath(t, root, dir), func(t *testing.T) {
				checkGoPackageDoc(t, dir)
			})
		}
	})

	t.Run("源码里的路径横幅", func(t *testing.T) {
		for _, file := range sourceFiles(t, root) {
			t.Run(relativePath(t, root, file), func(t *testing.T) {
				checkSourceBanner(t, file)
			})
		}
	})
}

// checkGoPackageDoc 检查一个目录下的 Go 包的包文档是否合规。
func checkGoPackageDoc(t *testing.T, dir string) {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		t.Fatalf("解析源码失败：%v", err)
	}

	for name, pkg := range pkgs {
		// 一个包只应有一个文件带包注释：go/doc 会把这些注释按文件名序
		// 依次拼起来，多个文件各写一段就会让读者在 godoc 里看到重复段落。
		var withDoc []string
		for filename, file := range pkg.Files {
			if file.Doc != nil {
				withDoc = append(withDoc, filepath.Base(filename))
			}
		}
		sort.Strings(withDoc)
		if len(withDoc) != 1 {
			t.Errorf("包 %s 带包注释的文件有 %d 个（%s），应恰好有一个：多个会被 go/doc 拼成重复段落，没有则读者在 godoc 里看不到任何说明",
				name, len(withDoc), strings.Join(withDoc, "、"))
		}

		pkgDoc := doc.New(pkg, "", doc.AllDecls)
		if pkgDoc.Doc == "" {
			// 包文档是可选的：没有就没有，但一旦写了就必须合规。
			continue
		}

		first := firstNonEmptyLine(pkgDoc.Doc)
		mainOK := name == "main" && strings.HasPrefix(first, "Command ")
		if !strings.HasPrefix(first, "Package "+name) && !mainOK {
			t.Errorf("包 %s 的文档首行 = %q，期望以 %q 开头", name, first, "Package "+name)
		}
	}
}

// checkSourceBanner 检查文件里没有"整行只有一个文件路径"的注释横幅。
func checkSourceBanner(t *testing.T, path string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取文件失败：%v", err)
	}

	marker := "//"
	if strings.HasSuffix(path, ".sql") {
		marker = "--"
	}

	for i, line := range strings.Split(string(content), "\n") {
		text, ok := commentText(line, marker)
		if !ok {
			continue
		}
		if pathBannerPattern.MatchString(text) {
			t.Errorf("第 %d 行是路径横幅：%q", i+1, text)
		}
	}
}

// commentText 判断这一行是不是整行注释；是的话剥掉注释标记只留正文。
func commentText(line, marker string) (string, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, marker) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(trimmed, marker)), true
}

// goPackageDirs 列出仓库里含非测试 Go 源文件的目录。
//
// 只有测试文件的目录不算：解析不出包文档，检查没有意义。
func goPackageDirs(t *testing.T, root string) []string {
	t.Helper()

	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if path != root && skipDir(d.Name()) {
			return fs.SkipDir
		}
		if hasGoSource(path) {
			dirs = append(dirs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓库目录失败：%v", err)
	}
	return dirs
}

// sourceFiles 列出仓库里所有 Go 与 SQL 源文件。
func sourceFiles(t *testing.T, root string) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && skipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".sql") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("扫描仓库文件失败：%v", err)
	}
	return files
}

// skipDir 判断目录名是否应当跳过（隐藏目录与第三方目录）。
func skipDir(name string) bool {
	return strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules"
}

// hasGoSource 判断目录里是否有非测试的 Go 源文件。
func hasGoSource(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// repoRoot 从当前目录向上找到 go.mod 所在目录，即仓库根。
func repoRoot(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取当前目录失败：%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("未找到仓库根目录（向上没有 go.mod）")
		}
		dir = parent
	}
}

// relativePath 把绝对路径转成相对仓库根的路径，用作子测试名。
func relativePath(t *testing.T, root, path string) string {
	t.Helper()

	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("计算相对路径失败：%v", err)
	}
	return rel
}

// firstNonEmptyLine 取正文的第一行非空内容。
func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
