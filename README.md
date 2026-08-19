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
./lifely serve --owner manual   # http://127.0.0.1:7777
./lifely status                 # diz se o painel está de pé, e onde
./lifely stop --owner manual    # pede o fecho do painel
```

`--owner` é **obrigatório** em `serve` e `stop` — o binário não adivinha quem
está pedindo. Um TTY não prova que há uma pessoa digitando (cron, CI e um hook
do tribunal também têm um), e é essa resposta que decide se o fecho da sessão
do tribunal pode derrubar um painel que você subiu à mão: `tribunal` só derruba
o que é `tribunal`.

**Códigos de saída**: `0` fez · `1` falhou · **`3` recusou deliberadamente** —
o painel continua de pé e o motivo já foi impresso. Um script que fecha o
tribunal precisa dos três separados: "parei", "quebrei" e "não era meu para
parar" são decisões diferentes.

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
