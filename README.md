# lifely

Painel local das pendências da casa e **orquestrador do desenvolvimento**.

O lifely varre as fontes de pendência (o repo do tribunal e os vaults do
ject), mostra o que espera decisão agrupado por **quem bloqueia**, e dirige as
sessões de desenvolvimento — sempre pedindo a sessão ao ject, nunca por fora
dele.

> **Estado hoje:** o quadro sai por `lifely scan`, no terminal. O servidor
> HTTP responde `/healthz` e serve a casca do painel; as telas ainda não
> renderizam a varredura. O parágrafo acima descreve o produto da spec do
> `lifely-001`, não o que já está de pé.

Duas coisas que ele nunca faz: **dar veredito de [DIREÇÃO]** (quem grava é a
superfície dona, com o fundador no meio) e **guardar estado de domínio** — a
verdade vive nos arquivos do tribunal, no ject e no store do Claude.

## Subir

```sh
go build ./cmd/lifely
./lifely serve            # http://127.0.0.1:7777
./lifely status           # diz se o painel está de pé, e onde
./lifely stop             # pede o fecho do painel
./lifely scan             # varre as fontes e imprime o quadro no terminal
```

O servidor escuta **só em loopback** e recusa requisição cujo `Host` não seja
um nome de loopback: uma página de outro site não alcança a porta pelo
navegador. Não há autenticação, e não deve haver.

Só existe **uma instância**: `serve` com o painel já de pé não sobe um
segundo — reusa o que está rodando e diz a URL.

O `/tribunal` sobe o painel no início da sessão e o derruba no fim — mas
**só derruba a instância que ele mesmo subiu** (`stop --owner tribunal`).
Servidor iniciado à mão sobrevive ao fecho da sessão.

## Varrer sem o painel

`lifely scan` faz a mesma varredura que o painel e imprime o resultado: as
pendências agrupadas por quem bloqueia e, depois, o estado de cada fonte —
fonte que existe e não pôde ser lida sai **marcada**, nunca some, inclusive
quando não há nenhuma pendência. O repo do tribunal vem de `--root` (padrão
`~/projects/artifacts`); os tickets vêm do binário `ject` no `PATH` — sem ele,
a fonte `ject` aparece ilegível, não vazia.

## Desenvolvimento

```sh
go build ./...    # compila
go test ./...     # testes
go vet ./...      # lint
gofmt -w .        # formato
```

Este repo é **gated**: todo push sai por `git push no-mistakes`.

## Onde mora a verdade

A spec é `lifely-001` no vault do ject
(`specs/requirements.md`), aprovada pelo fundador em 18-08-2026. O registro
da decisão está no `life.md` §17.3 (2.5.24). Divergência entre este README e a
spec: a spec vence.
