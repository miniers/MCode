package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// piPackageSkillRoots 返回 pi settings.json 里 packages 声明的技能根目录。
// pi 会把包里的 skills 根目录递归展开，所以这里只解析到「根目录」这一层。
//
// ok=false 表示有声明无法在不猜测的前提下解析（对象形式带资源过滤、未知来源、
// 读不出的 package.json）。调用方必须退回 pi 原生发现：少解析一个包就等于悄悄
// 丢掉用户没停用的技能，那比多注入几段上下文更糟。
func piPackageSkillRoots(agentDir, workDir string) ([]string, bool) {
	decls, ok := piPackageDeclarations(agentDir, workDir)
	if !ok {
		return nil, false
	}
	seen := make(map[string]bool)
	roots := make([]string, 0, len(decls))
	for _, decl := range decls {
		pkgDir, ok := piPackageDir(agentDir, decl)
		if !ok {
			return nil, false
		}
		if info, err := os.Stat(pkgDir); err != nil || !info.IsDir() {
			// 声明了但没装上：pi 自己会告警并跳过，这里同样跳过。
			continue
		}
		dirs, ok := piPackageSkills(pkgDir)
		if !ok {
			return nil, false
		}
		for _, dir := range dirs {
			abs := piAbsClean(dir)
			if abs == "" || seen[abs] {
				continue
			}
			seen[abs] = true
			roots = append(roots, abs)
		}
	}
	sort.Strings(roots)
	return roots, true
}

// piPackageDeclarations 收集全局与项目 settings.json 里的 packages 来源。
// 对象形式只有在没有资源过滤时才算可解析，其余一律交给调用方退回原生发现。
func piPackageDeclarations(agentDir, workDir string) ([]string, bool) {
	var decls []string
	for _, path := range []string{
		filepath.Join(agentDir, "settings.json"),
		filepath.Join(workDir, ".pi", "settings.json"),
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, false
		}
		var cfg struct {
			Packages []json.RawMessage `json:"packages"`
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return nil, false
		}
		for _, raw := range cfg.Packages {
			var src string
			if err := json.Unmarshal(raw, &src); err == nil {
				decls = append(decls, piResolveLocalSource(src, filepath.Dir(path)))
				continue
			}
			var obj struct {
				Source     string          `json:"source"`
				Extensions json.RawMessage `json:"extensions"`
				Skills     json.RawMessage `json:"skills"`
				Prompts    json.RawMessage `json:"prompts"`
				Themes     json.RawMessage `json:"themes"`
			}
			if err := json.Unmarshal(raw, &obj); err != nil || strings.TrimSpace(obj.Source) == "" {
				return nil, false
			}
			if obj.Extensions != nil || obj.Skills != nil || obj.Prompts != nil || obj.Themes != nil {
				return nil, false
			}
			decls = append(decls, piResolveLocalSource(obj.Source, filepath.Dir(path)))
		}
	}
	return decls, true
}

// piResolveLocalSource 把相对路径的本地包声明解析成绝对路径；其余来源原样返回。
func piResolveLocalSource(src, settingsDir string) string {
	src = strings.TrimSpace(src)
	if src == "." || src == ".." || strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
		return filepath.Join(settingsDir, src)
	}
	if strings.HasPrefix(src, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, src[2:])
		}
	}
	return src
}

// piPackageDir 把一条包声明映射到 pi 的安装目录。
func piPackageDir(agentDir, src string) (string, bool) {
	switch {
	case strings.HasPrefix(src, "npm:"):
		name := piNpmPackageName(strings.TrimPrefix(src, "npm:"))
		if name == "" {
			return "", false
		}
		return filepath.Join(agentDir, "npm", "node_modules", filepath.FromSlash(name)), true
	case strings.HasPrefix(src, "git:"):
		return piGitPackageDir(agentDir, strings.TrimPrefix(src, "git:"))
	case strings.HasPrefix(src, "https://"), strings.HasPrefix(src, "http://"):
		return piGitPackageDir(agentDir, src)
	case filepath.IsAbs(src):
		return filepath.Clean(src), true
	}
	return "", false
}

// piNpmPackageName 去掉版本号：npm:pkg@^1.2 → pkg，npm:@scope/pkg@1.0.0 → @scope/pkg。
func piNpmPackageName(spec string) string {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return ""
	}
	start := 0
	if strings.HasPrefix(spec, "@") {
		start = 1
	}
	if i := strings.Index(spec[start:], "@"); i >= 0 {
		spec = spec[:start+i]
	}
	return strings.Trim(spec, "/")
}

// piGitPackageDir 按 pi 的克隆布局拼出目录：<agentDir>/git/<host>/<owner>/<repo>。
func piGitPackageDir(agentDir, src string) (string, bool) {
	path := strings.TrimSpace(src)
	path = strings.TrimPrefix(path, "https://")
	path = strings.TrimPrefix(path, "http://")
	if i := strings.Index(path, "@"); i >= 0 {
		path = path[:i]
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.Trim(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return "", false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	return filepath.Join(append([]string{agentDir, "git"}, parts...)...), true
}

// piPackageSkills 返回一个包声明的技能路径（目录或 .md）。未声明 pi.skills 时
// 退回约定目录 <pkg>/skills。声明里有过滤语法时返回 ok=false。
func piPackageSkills(pkgDir string) ([]string, bool) {
	declared, ok := piPackageSkillManifest(pkgDir)
	if !ok {
		return nil, false
	}
	if declared == nil {
		conventional := filepath.Join(pkgDir, "skills")
		if info, err := os.Stat(conventional); err == nil && info.IsDir() {
			return []string{conventional}, true
		}
		return nil, true
	}
	var out []string
	for _, entry := range declared {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "!") || strings.HasPrefix(entry, "+") || strings.HasPrefix(entry, "-") {
			return nil, false
		}
		target := entry
		if !filepath.IsAbs(target) {
			target = filepath.Join(pkgDir, entry)
		}
		if strings.ContainsAny(target, "*?[") {
			matches, err := filepath.Glob(target)
			if err != nil {
				return nil, false
			}
			out = append(out, matches...)
			continue
		}
		out = append(out, target)
	}
	return out, true
}

// piPackageSkillManifest 读 package.json 的 pi.skills。没有 pi 键时返回 nil,nil；
// 有 pi 键但没有 skills 时返回空切片（约定目录不生效）。
func piPackageSkillManifest(pkgDir string) ([]string, bool) {
	data, err := os.ReadFile(filepath.Join(pkgDir, "package.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, true
		}
		return nil, false
	}
	var pkg struct {
		Pi *struct {
			Skills []string `json:"skills"`
		} `json:"pi"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return nil, false
	}
	if pkg.Pi == nil {
		return nil, true
	}
	return pkg.Pi.Skills, true
}
