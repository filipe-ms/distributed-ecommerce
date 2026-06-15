# Guia de Execução

Este documento explica como rodar o sistema localmente em **UNIX**
(macOS ou Linux) ou **WSL** (no Windows) e como usar o dashboard para simular e se recuperar de uma queda de serviço.

> **Windows puro não é suportado.** Use WSL — `make`, `bash` e o script
> de geração de certificado dependem de um shell UNIX.

## 1. Caminho rápido

Tendo o Docker instalado, dois comandos:

```bash
git clone <repo> && cd projeto
make
```

O alvo padrão (`make` = `make up`) faz o pacote inteiro:

1. Copia `.env` a partir do `.env.example` se faltar
2. Gera `certs/cert.pem` + `certs/key.pem` se faltarem (válido 365 dias)
3. Roda `docker compose up --build`

A primeira build leva uns 90 segundos (download dos módulos Go +
compilação). As próximas reusam cache e sobem em poucos segundos.

Quando tudo estiver no ar, abra:

| URL                                | Pra quê                                               |
| ---------------------------------- | ----------------------------------------------------- |
| <https://localhost:8443/>          | Loja — cadastro, login, ver produtos, fazer pedido    |
| <https://localhost:8443/estoque>   | Estoque — cadastrar produto e ver quantidades (admin) |
| <https://localhost:8443/dashboard> | Status dos serviços + simulação de quedas             |

O navegador vai reclamar do certificado auto-assinado na primeira vez
— é só aceitar.

## 2. Pré-requisitos

| Ferramenta                 | Versão testada               |
| -------------------------- | ---------------------------- |
| Docker (Engine ou Desktop) | 24.x ou mais novo            |
| Plugin Docker Compose      | v2.x                         |
| `make`                     | qualquer versão (BSD ou GNU) |

`curl` e `jq` deixam o fluxo de demo via terminal mais legível, mas são
opcionais. **Não precisa instalar Go na máquina** — o build acontece
dentro da imagem `golang:1.22-alpine`.

### macOS

```bash
brew install --cask docker
```

Abra o Docker Desktop uma vez (ele pede permissão de admin pra
inicializar). Quando o ícone estabilizar na barra de menu, está pronto.

### Linux

Instale o Docker Engine pela [documentação
oficial](https://docs.docker.com/engine/install/). Resumo no Ubuntu:

```bash
sudo apt-get update
sudo apt-get install docker.io docker-compose-plugin make
sudo usermod -aG docker $USER
# faça logout/login pra valer a alteração de grupo
```

### Windows (WSL)

Use o **Docker Desktop com integração WSL2 habilitada** e rode todos
os comandos deste guia de dentro do shell Ubuntu (ou outra distro WSL).
A partir daí o fluxo é idêntico ao Linux.

## 3. Targets do Makefile

Tudo que importa tá ali. `make help` lista também:

```bash
make             # padrão = make up (gera certs/.env e sobe a stack)
make down        # para os containers, mantém volumes
make clean       # para os containers e apaga os volumes
make rebuild     # rebuild sem cache e sobe
make logs        # acompanha os logs de todos os serviços
make test        # roda go test ./... dentro da imagem golang:1.22-alpine
make status      # mostra o estado dos containers
make dashboard   # abre o dashboard no navegador (open / xdg-open)
make certs       # gera o cert TLS se não existir
make certs-force # sempre regera o cert TLS
```

Quando tudo estiver no ar, cinco containers estão rodando:

| Container          | Porta interna      | Observação                                     |
| ------------------ | ------------------ | ---------------------------------------------- |
| `gateway`          | `8443` (publicada) | Único ponto de entrada, serve loja + dashboard |
| `users`            | `5001`             | Serviço de usuários (SQLite)                   |
| `products-primary` | `5002`             | Réplica A do catálogo                          |
| `products-replica` | `5012`             | Réplica B do catálogo                          |
| `orders`           | `5003`             | Serviço de pedidos (SQLite)                    |

## 4. Caso prefira não usar `make`

Os comandos manuais equivalentes (mesmo efeito do `make`):

```bash
cp .env.example .env                    # equivale a 'make envfile'
bash certs/generate.sh                  # equivale a 'make certs'
docker compose up --build               # equivale a 'make up'
docker compose down                     # equivale a 'make down'
docker compose down -v                  # equivale a 'make clean'
```

Útil em ambiente sem `make`, mas em UNIX e WSL `make` está sempre
presente ou é trivial de instalar.

## 5. Contas e produtos seedados

Na primeira subida, o serviço de usuários cria automaticamente duas
contas pra demonstração:

| Login                      | Papel                          |
| -------------------------- | ------------------------------ |
| `admin@local` / `admin123` | admin (pode cadastrar produto) |
| `user@local` / `user123`   | usuário comum (pode pedir)     |

E o serviço de produtos popula o catálogo com 5 itens × 10 unidades
cada (Barbeador, Caderno, Chave de fenda, Desodorante, Bandeirinha de
São João). Suficiente pra fazer login, pedir e ver o estoque cair.

Se quiser começar do zero: `make clean && make` — apaga volumes e
re-seeda tudo na próxima subida.

## 6. O frontend

A loja em <https://localhost:8443/> tem três abas no topo:

- **Loja** — quando logado, lista os produtos com quantidade e o botão
  _Pedir_. Cada pedido chama dois endpoints: primeiro
  `POST /products/{id}/decrement` (replicado nas duas réplicas) e
  depois `POST /orders` (registra o pedido). A seção _Meus pedidos_
  abaixo mostra o histórico do usuário logado.
- **Dashboard dos serviços** — grade de status dos quatro serviços
  monitorados. Cada linha tem um botão _Kill_ que aciona o kill switch
  (`POST /admin/toggle`); o serviço sai e o `restart: unless-stopped`
  do Compose recoloca ele no ar em segundos. O heartbeat detecta a
  queda e registra `DOWN` / `RECOVERED` na lista de eventos.
- **Estoque** — só visível pra admin. Form simples (nome + quantidade)
  pra cadastrar produto, e tabela com o estoque atual.

## 7. Fluxo de demo via curl (alternativa ao frontend)

A mesma demonstração pelo terminal. A flag `-k` aceita o cert
auto-assinado.

```bash
# (1) Login como admin
ADMIN_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"admin@local","password":"admin123"}' \
     | jq -r .token)

# (2) Ver os produtos seedados
curl -k https://localhost:8443/api/products | jq

# (3) Cadastrar um produto novo (só admin)
curl -k -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $ADMIN_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Café","price":9.99,"description":"Arabica","quantity":15}'

# (4) Conferir que as duas réplicas têm o produto
docker compose exec products-primary cat /data/products.json
docker compose exec products-replica cat /data/products.json

# (5) Login como usuário comum
USER_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"user@local","password":"user123"}' \
     | jq -r .token)
USER_ID=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"user@local","password":"user123"}' \
     | jq -r .user.id)

# (6) Fazer um pedido — front faz decrement + orders, então o curl
#     também precisa fazer os dois pra refletir o mesmo fluxo
curl -k -X POST https://localhost:8443/api/products/1/decrement \
     -H "authorization: Bearer $USER_TOKEN"
curl -k -X POST https://localhost:8443/api/orders \
     -H "authorization: Bearer $USER_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"productId":1}'

# (7) Listar os pedidos do usuário
curl -k -H "authorization: Bearer $USER_TOKEN" \
     "https://localhost:8443/api/orders/$USER_ID"
```

Caminhos negativos pra demonstrar a proteção JWT:

```bash
# Usuário comum não pode criar produto: HTTP 403
curl -k -i -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $USER_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Tea","price":4.5,"quantity":5}'

# Usuário não pode ver pedidos de outra pessoa: HTTP 403
curl -k -i -H "authorization: Bearer $USER_TOKEN" \
     https://localhost:8443/api/orders/9999
```

## 8. Simulando uma queda

1. Abra o dashboard.
2. Clique **Kill** ao lado de `orders`.
3. Em ~10 segundos o indicador fica vermelho e aparece a linha de
   evento `<timestamp>  orders  DOWN`.
4. Enquanto o serviço tá fora, qualquer chamada em
   `https://localhost:8443/api/orders/...` devolve **HTTP 503** — o
   gateway curto-circuita a request porque o heartbeat marcou o serviço
   como indisponível.
5. Alguns segundos depois o container reinicia sozinho, o indicador
   volta pra verde e um evento `RECOVERED` é adicionado.

> A janela real em que dá pra ver o 503 é curta (uns 5–10 segundos)
> porque o `restart: unless-stopped` levanta o container antes do
> heartbeat reagir totalmente. Pra forçar uma queda longa e ver o 503
> com calma: `docker compose stop orders` (sem `down`), espera uns 15
> segundos, faz a request. Restaura com `docker compose start orders`.

Repita o experimento com qualquer dos quatro serviços. Matar
`products-primary` é particularmente interessante: as leituras
continuam (porque o `products-replica` ainda atende), mas as escritas
devolvem **HTTP 500** — a consistência forte exige que as duas
réplicas confirmem.

## 9. Testes

```bash
make test
```

Roda `go test ./...` dentro da imagem `golang:1.22-alpine`. Não precisa
de Go local. Saída esperada: verde em `internal/authentication`,
`internal/httpjson`, `internal/killswitch`, `internal/users`,
`internal/products`, `internal/orders` e `internal/gateway`.

## 10. Layout do projeto

```
.
├── certs/              geração do certificado auto-assinado
├── cmd/
│   ├── gateway/        entrypoint do gateway
│   ├── users/          entrypoint do serviço de usuários
│   ├── products/       entrypoint do serviço de produtos (usado 2x no compose)
│   └── orders/         entrypoint do serviço de pedidos
├── internal/
│   ├── authentication/ JWT + bcrypt + middlewares
│   ├── httpjson/       helpers de leitura/escrita de JSON
│   ├── killswitch/     implementação do /admin/toggle
│   ├── tlsserver/      wrapper do servidor HTTPS e do client interno
│   ├── users/          handlers e store SQLite do usuário
│   ├── products/       handlers e store em JSON do produto
│   ├── orders/         handlers e store SQLite do pedido
│   └── gateway/
│       ├── proxy.go, heartbeat.go, replica.go, server.go, dashboard.go, events.go
│       └── web/        index.html (loja), estoque.html, dashboard.html
├── Dockerfile          imagem única usada por todos os serviços
├── docker-compose.yml  cinco containers, uma rede, quatro volumes
├── Makefile            atalhos pra docker compose
├── relatorio.pdf       relatório respondendo as 5 perguntas
└── README_execucao.md  você está aqui
```

## 11. O que olhar enquanto avalia

- **Consistência forte:** `internal/gateway/replica.go` —
  `HandleWrite` manda pra duas réplicas e só responde sucesso quando
  as duas devolvem 2xx.
- **Heartbeat:** `internal/gateway/heartbeat.go` — poll de 5 segundos,
  marca DOWN depois de duas falhas seguidas, marca RECOVERED no
  primeiro sucesso depois disso.
- **JWT e proteção de admin:**
  `internal/authentication/authentication.go` (assinatura/verificação)
  e `internal/products/server.go` (grupo de rotas só pra admin).
- **Hash de senha:** `HashPassword` / `VerifyPassword` em
  `internal/authentication/authentication.go`, usado em todos os
  pontos de entrada de `internal/users/handlers.go`.
- **Frontend:** `internal/gateway/web/index.html`,
  `internal/gateway/web/estoque.html`, embutidos no binário do gateway
  via `//go:embed` em `internal/gateway/dashboard.go`.

O **`relatorio.pdf`** que vem junto responde as cinco perguntas do
enunciado e discute os trade-offs (consistência forte vs. eventual,
chave única de JWT compartilhada, `InsecureSkipVerify` nas chamadas
internas etc.).
