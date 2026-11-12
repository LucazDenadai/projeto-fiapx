// PostToolUse hook: formata com gofmt o arquivo .go que o agente acabou de editar.
// Silencioso quando não é .go ou quando gofmt não está no PATH.
const { execFileSync } = require("child_process");

let input = "";
process.stdin.on("data", (c) => (input += c));
process.stdin.on("end", () => {
  try {
    const file = JSON.parse(input)?.tool_input?.file_path;
    if (!file || !file.endsWith(".go")) return;
    execFileSync("gofmt", ["-w", file], { stdio: "ignore" });
  } catch {
    // não bloqueia a edição se gofmt falhar; `go vet`/CI pegam o resto
  }
});
