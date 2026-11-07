package remote

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

// TestAlwaysAllowStore_AddListRevoke 覆盖增、查、列、撤销与空参数处理。
func TestAlwaysAllowStore_AddListRevoke(t *testing.T) {
	dir := t.TempDir()
	project := normalizeAlwaysAllowProject(dir)
	store := newAlwaysAllowStore(alwaysAllowStorePath(dir))

	if store.Allowed(project, "bash\x00ls -la") {
		t.Fatal("空存储不应命中任何记账键")
	}
	if err := store.Add(project, "bash\x00ls -la"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if err := store.Add(project, "bash\x00ls -la"); err != nil {
		t.Fatalf("重复 Add 应幂等: %v", err)
	}
	if err := store.Add(project, "write"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if !store.Allowed(project, "bash\x00ls -la") {
		t.Fatal("已记录的记账键应命中")
	}
	if got, want := store.List(project), []string{"bash\x00ls -la", "write"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("List = %v, want %v", got, want)
	}
	all := store.ListAll()
	if len(all) != 1 || len(all[project]) != 2 {
		t.Fatalf("ListAll = %v, want 单个项目两条记录", all)
	}

	if err := store.Remove(project, "bash\x00ls -la"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if store.Allowed(project, "bash\x00ls -la") {
		t.Fatal("撤销后不应再命中")
	}
	if err := store.Remove(project, "never-added"); err != nil {
		t.Fatalf("撤销不存在的键应幂等: %v", err)
	}
	if err := store.Remove(project, "write"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if got := store.ListAll(); len(got) != 0 {
		t.Fatalf("全部撤销后 ListAll 应为空, got %v", got)
	}

	// 空项目、空键都不记账；不同项目互不影响
	if err := store.Add("", "x"); err != nil {
		t.Fatalf("空项目 Add 应直接返回: %v", err)
	}
	if err := store.Add(project, ""); err != nil {
		t.Fatalf("空键 Add 应直接返回: %v", err)
	}
	if len(store.ListAll()) != 0 {
		t.Fatal("空项目或空键不应落账")
	}
	if err := store.Add(project, "write"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if store.Allowed(t.TempDir(), "write") {
		t.Fatal("其他项目不应命中")
	}
}

// TestAlwaysAllowStore_AtomicWriteAndPermissions 覆盖落盘：0600 权限、无残留临时文件、内容可解析。
func TestAlwaysAllowStore_AtomicWriteAndPermissions(t *testing.T) {
	dir := t.TempDir()
	path := alwaysAllowStorePath(dir)
	project := normalizeAlwaysAllowProject(dir)
	store := newAlwaysAllowStore(path)
	if err := store.Add(project, "bash\x00pwd"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat store file failed: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("store file perm = %v, want 0600", perm)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir failed: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != alwaysAllowFileName {
		t.Fatalf("数据目录应只剩目标文件, got %v", entries)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read store file failed: %v", err)
	}
	var file alwaysAllowFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatalf("落盘内容应为合法 JSON: %v", err)
	}
	if got := file.Projects[project]; !reflect.DeepEqual(got, []string{"bash\x00pwd"}) {
		t.Fatalf("落盘内容 = %v, want 单条 bash\\x00pwd", got)
	}
}

// TestAlwaysAllowStore_CorruptedFileTolerated 覆盖文件损坏：按空处理、保留 .bak、之后仍可正常写入。
func TestAlwaysAllowStore_CorruptedFileTolerated(t *testing.T) {
	dir := t.TempDir()
	path := alwaysAllowStorePath(dir)
	project := normalizeAlwaysAllowProject(dir)
	if err := os.WriteFile(path, []byte("{ not json"), 0o600); err != nil {
		t.Fatalf("写入损坏文件失败: %v", err)
	}

	store := newAlwaysAllowStore(path)
	if store.Allowed(project, "bash\x00ls") {
		t.Fatal("损坏文件应按空存储处理")
	}
	if got := store.ListAll(); len(got) != 0 {
		t.Fatalf("损坏文件应按空存储处理, got %v", got)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatalf("损坏文件应保留为 .bak: %v", err)
	}
	if string(backup) != "{ not json" {
		t.Fatalf(".bak 内容 = %q, want 原始损坏内容", backup)
	}

	if err := store.Add(project, "bash\x00ls"); err != nil {
		t.Fatalf("损坏后仍应可写入: %v", err)
	}
	if reloaded := newAlwaysAllowStore(path); !reloaded.Allowed(project, "bash\x00ls") {
		t.Fatal("损坏恢复后写入的记录应能重新加载")
	}
}

// TestAlwaysAllowStore_ConcurrentAdd 覆盖并发写入：不丢键、并发安全、落盘仍是合法 JSON。
func TestAlwaysAllowStore_ConcurrentAdd(t *testing.T) {
	dir := t.TempDir()
	path := alwaysAllowStorePath(dir)
	project := normalizeAlwaysAllowProject(dir)
	store := newAlwaysAllowStore(path)

	const n = 24
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.Add(project, "bash\x00cmd-"+strconv.Itoa(i)); err != nil {
				t.Errorf("并发 Add 失败: %v", err)
			}
		}(i)
	}
	wg.Wait()

	if got := len(store.List(project)); got != n {
		t.Fatalf("并发 Add 后记录数 = %d, want %d", got, n)
	}
	reloaded := newAlwaysAllowStore(path)
	if got := len(reloaded.List(project)); got != n {
		t.Fatalf("重新加载后记录数 = %d, want %d（有写入被覆盖）", got, n)
	}
}

// TestAlwaysAllowStore_ReloadAcrossInstances 覆盖「重启」：新实例读同一文件能命中旧实例写入的键。
func TestAlwaysAllowStore_ReloadAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	path := alwaysAllowStorePath(dir)
	project := normalizeAlwaysAllowProject(dir)

	first := newAlwaysAllowStore(path)
	if err := first.Add(project, "bash\x00make test"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	second := newAlwaysAllowStore(path)
	if !second.Allowed(project, "bash\x00make test") {
		t.Fatal("新实例应能读到旧实例写入的记账键")
	}
	if err := second.Remove(project, "bash\x00make test"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}
	if third := newAlwaysAllowStore(path); third.Allowed(project, "bash\x00make test") {
		t.Fatal("撤销结果应落盘，新实例不应再命中")
	}
}

// TestAlwaysAllowStore_NormalizesProjectPath 覆盖项目键规范：符号链接与冗余路径段都指向同一桶。
func TestAlwaysAllowStore_NormalizesProjectPath(t *testing.T) {
	root := t.TempDir()
	realDir := filepath.Join(root, "project")
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatalf("创建项目目录失败: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(realDir, link); err != nil {
		t.Skipf("当前环境不支持符号链接: %v", err)
	}

	store := newAlwaysAllowStore(alwaysAllowStorePath(t.TempDir()))
	if err := store.Add(link, "bash\x00ls"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if !store.Allowed(realDir, "bash\x00ls") {
		t.Fatal("符号链接写入的键应能按真实路径命中")
	}
	if !store.Allowed(realDir+string(filepath.Separator)+".", "bash\x00ls") {
		t.Fatal("冗余路径段应归一到同一项目桶")
	}
	if !store.Allowed(link, "bash\x00ls") {
		t.Fatal("符号链接路径应归一到同一项目桶")
	}
}

// TestAlwaysAllowStoreForSharesInstance 覆盖按数据目录复用实例：同一目录的两次访问看到同一份记录。
func TestAlwaysAllowStoreForSharesInstance(t *testing.T) {
	dataDir := t.TempDir()
	t.Cleanup(func() { forgetAlwaysAllowStore(dataDir) })
	project := t.TempDir()

	if err := alwaysAllowStoreFor(dataDir).Add(project, "bash\x00ls"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if !alwaysAllowStoreFor(dataDir).Allowed(project, "bash\x00ls") {
		t.Fatal("同一数据目录应复用同一存储实例")
	}

	// 数据目录为空时退化为只读内存存储：不落盘也不命中，避免把记录写到意料之外的路径。
	if alwaysAllowStoreFor("").Allowed(project, "bash\x00ls") {
		t.Fatal("空数据目录不应命中任何记录")
	}
	if err := alwaysAllowStoreFor("").Add(project, "bash\x00ls"); err != nil {
		t.Fatalf("空数据目录写入应直接返回: %v", err)
	}
}

// TestAlwaysAllowStoreFactoryInjection 覆盖包级创建函数的注入与还原：测试可把存储指到临时路径。
func TestAlwaysAllowStoreFactoryInjection(t *testing.T) {
	dataDir := t.TempDir()
	customPath := filepath.Join(t.TempDir(), "custom-always-allow.json")
	project := t.TempDir()

	previous := alwaysAllowStoreFactory
	alwaysAllowStoreFactory = func(string) *alwaysAllowStore { return newAlwaysAllowStore(customPath) }
	t.Cleanup(func() {
		alwaysAllowStoreFactory = previous
		forgetAlwaysAllowStore(dataDir)
	})

	if err := alwaysAllowStoreFor(dataDir).Add(project, "bash\x00ls"); err != nil {
		t.Fatalf("Add failed: %v", err)
	}
	if _, err := os.Stat(customPath); err != nil {
		t.Fatalf("注入的存储应写到指定路径 %s: %v", customPath, err)
	}
	if !newAlwaysAllowStore(customPath).Allowed(project, "bash\x00ls") {
		t.Fatal("注入路径写入的记录应能在新实例里命中")
	}
	if _, err := os.Stat(alwaysAllowStorePath(dataDir)); err == nil {
		t.Fatal("注入后默认数据目录不应出现持久化文件")
	}
}
