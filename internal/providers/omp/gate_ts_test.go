package omp

import (
	"os/exec"
	"testing"
)

func TestAutoGate(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("未安装 node")
	}
	const driver = `
const fs = require('node:fs');
(async () => {
  const source = fs.readFileSync('mcode-gate.ts', 'utf8');
  const gate = await import('data:text/javascript;base64,' + Buffer.from(source).toString('base64'));
  let handler;
  await gate.default({ registerCommand() {}, on(name, fn) { handler = fn; } });
  const file = process.env.MCODE_OMP_GATE;
  for (const mode of ['auto', 'autoAcceptEdits']) {
    fs.writeFileSync(file, JSON.stringify({runtimeMode: mode}));
    for (const [toolName, command, expected] of [
      ['write', '', 'allow'], ['edit', '', 'allow'], ['bash', 'ls', 'allow'],
      ['bash', 'git status', 'allow'], ['bash', 'go build', 'ask'],
      ['bash', 'git branch new', 'ask'], ['eval', 'ls', 'ask']
    ]) {
      let asked = false;
      const result = await handler({toolName, input:{command}}, {hasUI:true, ui:{select:async () => { asked = true; return 'Deny'; }}});
      const actual = asked ? 'ask' : result?.block ? 'block' : 'allow';
      if (actual !== expected) throw new Error(mode + ' ' + toolName + ' ' + command + ': ' + actual);
    }
  }
})().catch(err => { console.error(err); process.exit(1); });
`
	cmd := exec.Command(node, "-e", driver)
	cmd.Env = append(cmd.Environ(), "MCODE_OMP_GATE="+t.TempDir()+"/gate.json")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("门禁测试失败: %v: %s", err, output)
	}
}
