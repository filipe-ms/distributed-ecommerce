# Guia de Execução

Este documento explica como rodar o sistema localmente em **macOS** ou
**Linux**, mostra o fluxo de demo que exercita todos os requisitos do
trabalho e como usar o dashboard pra simular e se recuperar de uma
queda de serviço.

## 1. Pré-requisitos

A única coisa que precisa estar instalada é o Docker:

| Ferramenta | Versão testada |
|------------|----------------|
| Docker (Engine ou Desktop) | 24.x ou mais novo |
| Plugin Docker Compose | v2.x |

`curl` e `jq` deixam o fluxo de demo mais legível, mas são opcionais.
**Não precisa instalar Go na máquina** — o build acontece dentro da
imagem `golang:1.22-alpine`.

### macOS

A forma mais fácil é instalar o **Docker Desktop**:

```bash
brew install --cask docker
```

Depois abra o app uma vez (ele pede permissão de admin pra inicializar).
Quando o ícone do Docker estabilizar na barra de menu, está pronto.

### Linux (Ubuntu / Debian / Fedora)

Instale o Docker Engine seguindo o passo-a-passo da [documentação
oficial](https://docs.docker.com/engine/install/). Resumo no Ubuntu:

```bash
sudo apt-get update
sudo apt-get install docker.io docker-compose-plugin
sudo usermod -aG docker $USER
# faça logout/login pra valer a alteração de grupo
```

Depois confira que o `docker compose version` responde sem erro.

## 2. Configuração

A única coisa que o sistema precisa configurada é a chave secreta do
JWT. Copie o exemplo e ajuste se quiser:

```bash
cp .env.example .env
```

O valor padrão (`dev-secret-change-me-before-shipping`) já serve pro
trabalho; em produção você usaria uma string aleatória longa.

## 3. Gerando o certificado TLS

Todos os containers usam o mesmo certificado auto-assinado pra HTTPS.
O script abaixo gera um novo:

```bash
bash certs/generate.sh
```

Isso produz `certs/cert.pem` e `certs/key.pem`. Eles são copiados pra
imagem Docker no momento do build, então só precisa rodar uma vez (ou
de novo quando expirar, depois de um ano).

> **Nota:** o cert é válido pra `localhost` e pros nomes internos
> (`gateway`, `users`, `products-primary`, `products-replica`,
> `orders`). O navegador vai mostrar um aviso na primeira vez que você
> abrir o dashboard — é só aceitar e seguir.

## 4. Subindo o sistema

O repositório vem com um `Makefile` cujo alvo padrão faz o fluxo todo
— copia o `.env` do exemplo, gera os certs se não existirem e roda
`docker compose up --build`:

```bash
make
```

Se preferir chamar o compose direto, é exatamente o que o `make up`
roda por baixo:

```bash
docker compose up --build
```

A primeira build demora uns 90 segundos (download dos módulos Go +
compilação). Builds seguintes usam cache e sobem em poucos segundos.
Quando tudo estiver no ar, cinco containers vão estar rodando:

| Container | Porta interna | Notas |
|-----------|---------------|-------|
| `gateway` | `8443` (publicada) | Único ponto de entrada, também serve o dashboard |
| `users` | `5001` | Serviço de usuários (SQLite) |
| `products-primary` | `5002` | Réplica A do catálogo |
| `products-replica` | `5012` | Réplica B do catálogo |
| `orders` | `5003` | Serviço de pedidos (SQLite) |

Pra derrubar tudo: `make down` (ou `docker compose down`). Os volumes
são mantidos por padrão; `make clean` (ou `docker compose down -v`)
apaga também os bancos SQLite e o JSON de produtos.

Outros alvos úteis:

```bash
make help        # lista todos os alvos disponíveis
make rebuild     # rebuild sem cache e sobe
make logs        # acompanha os logs de todos os serviços
make test        # roda go test ./... dentro da imagem golang:1.22-alpine
make status      # mostra o estado dos containers
make dashboard   # abre https://localhost:8443/dashboard no navegador
```

## 5. O dashboard

Abra <https://localhost:8443/dashboard>. Depois de aceitar o cert
auto-assinado, aparece um quadro de status com quatro linhas — uma por
serviço — junto com os eventos recentes. Cada linha tem um botão:

* **Kill** — liga o kill switch daquele serviço. O serviço responde ao
  toggle e em ~500 ms desliga sozinho. A política
  `restart: unless-stopped` do Compose sobe ele de novo
  automaticamente; o heartbeat detecta a queda e registra os eventos
  `DOWN` e `RECOVERED`.
* **Revive** — aparece bem brevemente entre o kill e o restart
  automático. A maioria não chega a clicar, porque a recuperação é
  mais rápida que o reflexo.

A página chama `GET /administration/status` a cada dois segundos,
então o indicador fica vermelho em ~5–10 segundos depois da queda e
volta pra verde em ~5 segundos depois do container reiniciar.

## 6. Fluxo de demo com curl

A conta admin padrão é criada na primeira vez que o serviço de
usuários sobe:

```
email:    admin@local
senha:    admin123
```

Rode esses comandos em outro terminal com o sistema no ar. A flag
`-k` no curl aceita o cert auto-assinado.

```bash
# (1) Cadastrar um usuário comum.
curl -k -X POST https://localhost:8443/api/users/register \
     -H 'content-type: application/json' \
     -d '{"name":"Alice","email":"alice@example.com","password":"hunter2"}'

# (2) Login como admin.
ADMINISTRATOR_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"admin@local","password":"admin123"}' \
     | jq -r .token)

# (3) Criar um produto (só admin pode).
curl -k -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $ADMINISTRATOR_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Coffee","price":9.99,"description":"Arabica"}'

# (4) Conferir que as duas réplicas têm o produto.
docker compose exec products-primary cat /data/products.json
docker compose exec products-replica cat /data/products.json

# (5) Login como Alice e fazer um pedido.
ALICE_TOKEN=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"alice@example.com","password":"hunter2"}' \
     | jq -r .token)

curl -k -X POST https://localhost:8443/api/orders \
     -H "authorization: Bearer $ALICE_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"productId":1}'

# (6) Listar os pedidos da Alice.
ALICE_USER_ID=$(curl -ks -X POST https://localhost:8443/api/users/login \
     -H 'content-type: application/json' \
     -d '{"email":"alice@example.com","password":"hunter2"}' \
     | jq -r .user.id)

curl -k -H "authorization: Bearer $ALICE_TOKEN" \
     "https://localhost:8443/api/orders/$ALICE_USER_ID"
```

Os caminhos negativos também funcionam:

```bash
# Usuário comum não pode criar produto: HTTP 403.
curl -k -i -X POST https://localhost:8443/api/products \
     -H "authorization: Bearer $ALICE_TOKEN" \
     -H 'content-type: application/json' \
     -d '{"name":"Tea","price":4.5,"description":""}'

# Alice não pode ver os pedidos de outro usuário: HTTP 403.
curl -k -i -H "authorization: Bearer $ALICE_TOKEN" \
     https://localhost:8443/api/orders/9999
```

## 7. Simulando uma queda

1. Abra o dashboard.
2. Clique em **Kill** ao lado de `orders`.
3. Em ~10 segundos o indicador fica vermelho e aparece a linha de
   evento: `<timestamp>  orders  DOWN`.
4. Enquanto o serviço tá fora, qualquer chamada em
   `https://localhost:8443/api/orders` devolve **HTTP 503** — o gateway
   curto-circuita a request porque o heartbeat marcou o serviço como
   indisponível.
5. Alguns segundos depois o container reinicia sozinho. O indicador
   volta pra verde e um evento `RECOVERED` é adicionado.

Pode repetir o experimento com qualquer dos quatro serviços. Matar o
`products-primary` é particularmente interessante: as leituras
continuam (porque o `products-replica` ainda atende), mas as escritas
devolvem **HTTP 500** porque a consistência forte exige que as duas
réplicas confirmem.

## 8. Rodando os testes

A suíte de testes Go roda inteira dentro do Docker, então não precisa
ter Go instalado:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.22-alpine \
    sh -c 'go mod tidy && go test ./...'
```

Você deve ver saída verde nos pacotes `internal/authentication`,
`internal/httpjson`, `internal/killswitch`, `internal/users`,
`internal/products`, `internal/orders` e `internal/gateway`.

## 9. Layout do projeto

```
.
├── certs/              geração do certificado auto-assinado
├── cmd/
│   ├── gateway/        entrypoint do gateway (main.go)
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
│   └── gateway/        proxy, heartbeat, replica manager, dashboard
├── Dockerfile          imagem única usada por todos os serviços
├── docker-compose.yml  cinco containers, uma rede, quatro volumes
└── README_execucao.md  você está aqui
```

## 10. O que olhar enquanto avalia

* **Consistência forte:** `internal/gateway/replica.go` — o
  `HandleWrite` manda pra duas réplicas e só responde sucesso quando
  as duas devolvem 2xx.
* **Heartbeat:** `internal/gateway/heartbeat.go` — poll de 5 segundos,
  marca DOWN depois de duas falhas seguidas, marca RECOVERED no
  primeiro sucesso depois disso.
* **JWT e proteção de admin:**
  `internal/authentication/authentication.go` (assinatura/verificação)
  e `internal/products/server.go` (grupo de rotas só pra admin).
* **Hash de senha:** `HashPassword` / `VerifyPassword` em
  `internal/authentication/authentication.go`, usado em todos os
  pontos de entrada de `internal/users/handlers.go`.

O **relatorio.pdf** que vem junto responde as cinco perguntas do
enunciado e discute os trade-offs (consistência forte vs. eventual,
chave única de JWT compartilhada, `InsecureSkipVerify` nas chamadas
internas etc.).
