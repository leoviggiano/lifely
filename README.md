# lifely

Painel local das pendências da casa e **orquestrador do desenvolvimento**.

O lifely varre as fontes de pendência (o repo do tribunal e os vaults do
ject), mostra o que espera decisão agrupado por **quem bloqueia**, e dirige as
sessões de desenvolvimento — sempre pedindo a sessão ao ject, nunca por fora
dele.

> **Estado hoje:** o quadro sai por `lifely scan`, no terminal. O servidor
> HTTP responde `/healthz` e a **API de leitura** (`/api/pendencies`,
> `/api/pendencies/{id}`, `/api/sources`, `/api/projects`, `/api/sonar`), com
> spec OpenAPI gerada em `/swagger/openapi.json`. Existe **uma** tela: o
> **feed do sonar** em `/sonar` (`lifely-018`), a primeira coisa que o binário
> serve a partir do `web/` embutido. As **oito telas da FR4** (Mesa, Menu,
> Projeto, Ticket, Fonte, Detalhe, Despacho, Vazio) ainda não existem, e `/`
> segue sem rota **de propósito** — a raiz é da Mesa. O parágrafo acima
> descreve o produto da spec do `lifely-001`, não o que já está de pé.

Duas coisas que ele nunca faz: **dar veredito de [DIREÇÃO]** (quem grava é a
superfície dona, com o fundador no meio) e **guardar estado de domínio** — a
verdade vive nos arquivos do tribunal, no ject e no store do Claude.

## Subir

```sh
go build ./cmd/lifely
./lifely serve --owner manual   # http://127.0.0.1:7777 (aceita --port e --root)
./lifely status                 # diz se o painel está de pé, e onde
./lifely stop --owner manual    # pede o fecho do painel
./lifely scan                   # varre as fontes e imprime o quadro no terminal
```

`--owner` é **obrigatório** em `serve` e `stop` — o binário não adivinha quem
está pedindo. Um TTY não prova que há uma pessoa digitando (cron, CI e um hook
do tribunal também têm um), e é essa resposta que decide se o fecho da sessão
do tribunal pode derrubar um painel que você subiu à mão: `tribunal` só derruba
o que é `tribunal`.

`lifely scan` **sai 1 quando alguma fonte não pôde ser lida** — e o gatilho mais
comum não é repositório quebrado, é binário ausente: sem o `ject` no `PATH`, a
fonte `ject` aparece ilegível e a varredura é parcial. O quadro sai assim mesmo,
com o cabeçalho marcado `INCOMPLETE`; o código de saída existe para o script que
não pode tratar quadro parcial como quadro limpo.

Corte por orçamento de varredura é outra coisa: nenhuma fonte falhou, então o
cabeçalho sai marcado `PARTIAL` (e não `INCOMPLETE`) e a saída é `0` — o quadro
não está inteiro, mas nada quebrou.

**Códigos de saída**: `0` fez · `1` falhou (ou fonte ilegível na varredura) ·
**`3` recusou deliberadamente** —
o painel continua de pé e o motivo já foi impresso. Um script que fecha o
tribunal precisa dos três separados: "parei", "quebrei" e "não era meu para
parar" são decisões diferentes.

O servidor escuta **só em loopback** e recusa requisição cujo `Host` não seja
um nome de loopback: uma página de outro site não alcança a porta pelo
navegador. Não há autenticação, e não deve haver.

Só existe **uma instância**: `serve` com o painel já de pé não sobe um
segundo — reusa o que está rodando e diz a URL. O reuso **transfere a posse**
numa direção só: `serve --owner manual` sobre um painel do `tribunal` passa a
ser seu, e o fecho da sessão não o derruba mais.

O `/tribunal` sobe o painel no início da sessão e o derruba no fim — mas
**só derruba a instância que ele mesmo subiu** (`stop --owner tribunal`).
Servidor iniciado à mão sobrevive ao fecho da sessão.

## Feed do sonar

`http://127.0.0.1:7777/sonar` mostra a **história dos eventos da frota**: o
que o plantão do tribunal escreveu em `<root>/medicao/sonar.log`, mais recente
no topo, atualizando sozinho a cada ~3s. O mesmo feed em JSON sai por
`GET /api/sonar?project=<slug>&limit=<n>`.

Três coisas que a tela promete e são requisito, não estilo:

- **O lifely nunca escreve nesse log.** O pacote que o lê não tem caminho de
  escrita, e há um teste que anda a árvore sintática do pacote para manter
  isso verdade — a mesma classe da FR15, lida na direção oposta.
- **Linha que o parser não entende aparece crua**, marcada de vermelho, nunca
  descartada em silêncio. O log já mudou de formato uma vez (carimbo com e sem
  segundos, três formas de qualificador) e vai mudar de novo.
- **A idade do dado é visível.** Acima de 20 minutos — o dobro da cadência do
  sonar — o feed diz que o sonar está frio. Carimbo velho na tela é alarme por
  si só; barra que envelhece em silêncio mente.

O filtro por projeto casa o slug no texto da linha, porque o log declara o
projeto estruturalmente só em `portao` e `notify` e a notícia de verdade está
na prosa. As **opções** do filtro, ao contrário, saem só do que o log declara:
um falso positivo mostra uma linha a mais, mas uma opção falsa é um botão que
mente.

Sem toolchain de JS (FR4.11): o template e o CSS são embutidos com `embed`, e
a atualização é `fetch` de um fragmento renderizado pelo **mesmo** template
Go que pintou a primeira tela — um renderizador só, para que a primeira
pintura e o que a substitui não possam discordar.

## Varrer sem o painel

`lifely scan` faz a mesma varredura que o painel e imprime o resultado: as
pendências agrupadas por quem bloqueia e, depois, o estado de cada fonte —
fonte que existe e não pôde ser lida sai **marcada**, nunca some, inclusive
quando não há nenhuma pendência. Fonte lida pela metade também não some: sai
marcada `cut short`, mesmo sem nenhuma pendência aberta — "li tudo e não achei
nada" e "parei no meio" são respostas diferentes. O repo do tribunal vem de
`--root` (padrão `~/projects/artifacts`); os tickets vêm do binário `ject` no
`PATH` — sem ele, a fonte `ject` aparece ilegível, não vazia. O `--root` é o
mesmo do `serve`: o painel varre exatamente o que este comando varre.

## Desenvolvimento

```sh
go build ./...           # compila
go test ./...            # testes
go vet ./...             # lint
go run ./cmd/doclint .   # lint: doc no símbolo errado · cerca fora do md
gofmt -w .               # formato
```

Este repo é **gated**: todo push sai por `git push no-mistakes`. O portão roda
`test` e `lint` com `-trimpath` (comandos e motivo em `.no-mistakes.yaml`) —
teste que localize fixture por `runtime.Caller` passa aqui e quebra lá. O `lint`
do portão são os **dois** comandos acima: `go vet` mais o `doclint`, que recusa
comentário de doc que ficou no símbolo errado — inserir uma função entre o
comentário e sua declaração, um membro entre o comentário e a constante que
ele documenta dentro de um `const(...)`, ou um campo entre o comentário e o
campo de struct que ele descreve, não quebra compilação nem teste. O `doclint`
cobre toda declaração de topo: func, type, var e const soltos, cada spec
dentro de `const(...)`/`var(...)`/`type(...)`, e os campos de struct e métodos
de interface declarados num `type` (campo embutido responde pelo nome
implícito do tipo). Ficam de fora de propósito o import — um comentário só é
acusado quando a declaração que o carrega declara algum nome, e import não
declara nome que o lint indexe — e declaração dentro de corpo de função (esta
checagem só anda as declarações de topo do arquivo). O `cmd/doclint` diz por
que isso é lint, e não suíte, e o que fica fora do alcance.

O `doclint` tem uma **segunda checagem**: ele recusa uma máquina de estado de
cerca escrita fora da árvore de `internal/md`, a única onde essa guarda pode
morar. Três scanners já carregaram cópias da mesma guarda e as cópias
divergiram duas vezes; consolidá-las conserta as instâncias, e só uma trava
mecânica impede a quarta cópia. A assinatura que ela procura são as duas
metades inseparáveis da máquina **na mesma função**: o delimitador de cerca e
um valor que se nega a si mesmo (`x = !x`). As duas, ou nada — fixture de teste
com markdown cercado é dado, não guarda, e valor que alterna por outro motivo
não é cerca de ninguém. Os dois lados da negação são comparados **como
escritos**, não pelo tipo do nó: `s.inFence = !s.inFence` conta igual, porque
exigir um identificador puro deixava passar reta a máquina que guarda o flag
num campo. O delimitador conta escrito na linha **ou** içado para uma constante
ou variável de pacote (o nome é procurado no diretório inteiro, que é o alcance
de um pacote em Go): ler só o literal deixava a cópia a um refactor de ficar
invisível.

Nenhuma das duas checagens lê diretório que o `go build ./...` já deixa de fora
pelo nome — apontar o lint diretamente PARA um deles, porém, é pedido explícito
e é lido. O `cmd/doclint` enumera quais nomes são esses e diz qual exclusão do
`./...` a varredura ainda não sabe ver.

## Onde mora a verdade

A spec é `lifely-001` no vault do ject
(`specs/requirements.md`), aprovada pelo fundador em 18-08-2026. O registro
da decisão está no `life.md` §17.3 (2.5.24). Divergência entre este README e a
spec: a spec vence.

Tickets posteriores trazem specs próprias, e a numeração `FR` recomeça em cada
uma. `spec FR7` sem ticket, nos comentários deste repo, é sempre o do
`lifely-001` — ciclo de vida do servidor. O contrato do marcador de rodada
atravessada (A3, `carriesForward`) é o **FR7 do `lifely-028`**: outro ticket,
outro FR7, e é por isso que o comentário que o cita nomeia o ticket.
