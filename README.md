# lifely

Painel local das pendências da casa e **orquestrador do desenvolvimento**.

O lifely varre as fontes de pendência (o repo do tribunal e os vaults do
ject), mostra o que espera decisão agrupado por **quem bloqueia**, e dirige as
sessões de desenvolvimento — sempre pedindo a sessão ao ject, nunca por fora
dele.

Duas coisas que ele nunca faz: **dar veredito de [DIREÇÃO]** (quem grava é a
superfície dona, com o fundador no meio) e **guardar estado de domínio** — a
verdade vive nos arquivos do tribunal, no ject e no store do Claude.

## Subir

```sh
go build ./cmd/lifely
./lifely serve            # http://127.0.0.1:7777
```

O servidor escuta **só em loopback** e recusa requisição cujo `Host` não seja
um nome de loopback: uma página de outro site não alcança a porta pelo
navegador. Não há autenticação, e não deve haver.

O `/tribunal` sobe o painel no início da sessão e o derruba no fim — mas
**só derruba a instância que ele mesmo subiu**. Servidor iniciado à mão
sobrevive ao fecho da sessão.

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
