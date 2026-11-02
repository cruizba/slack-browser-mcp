# slack-browser-mcp

*[English](README.md) · Español*

Lectura y escritura en Slack para tu agente, con un inicio de sesión normal en el
navegador. Se integra en pi, Claude Code y opencode: dices lo que quieres — o lanzas
un comando `/slack…` — y el agente lo hace a través de un servidor MCP local.

> [!WARNING]
> **No oficial, y cada cual lo usa bajo su responsabilidad.** No es un producto de
> Slack y Slack no da soporte. Utiliza la API privada que hay detrás del cliente web
> de Slack, que Slack puede cambiar, limitar o bloquear en cualquier momento.
>
> **La autenticación es la cookie de sesión del navegador.** `slack-browser login`
> abre Chrome, captura el token `xoxc-…` y la cookie `xoxd-…` (la que se llama `d`)
> de una sesión ya iniciada, y las guarda en `~/.config/slack-browser-mcp/accounts`
> (`0600`). Ese par no tiene el alcance de un token de bot: lleva todo lo que puede
> hacer tu cuenta, incluido leer todos los canales privados en los que estés y
> publicar como tú. Trata ese almacén como una contraseña y no lo copies a ningún
> otro sitio.
>
> **Úsalo solo en espacios de trabajo que controles o que te hayan autorizado
> explícitamente a automatizar.** No lo apuntes a un espacio corporativo que no sea
> tuyo y no lo uses para saltarte las normas de un administrador sobre integraciones
> o automatización: puede incumplir las políticas de tu empresa y provocar la
> desactivación de la cuenta. Si eres admin del espacio, una app de Slack con un
> token de verdad es mejor opción; esta herramienta existe para los casos en los que
> no puedes instalar una. Mira las salvedades en
> [docs/reference.md](docs/reference.md).
>
> Sin garantía de ningún tipo; los autores no responden de lo que la herramienta
> haga con tu cuenta o tu espacio de trabajo. Publicado bajo la
> [licencia MIT](LICENSE).

**Linux, macOS y Windows**, en amd64 y arm64, desde un único binario estático sin
runtime que instalar. No hay servicio que habilitar ni init de por medio: el servidor
MCP del agente calienta el archivo al empezar la sesión, y una lectura que lo
encuentra viejo lanza un sync detrás de la respuesta. Necesita Google Chrome (o
Chromium, Edge, Brave, Vivaldi u Opera) sólo para el inicio de sesión.

## Instalación

```bash
curl -fsSL https://raw.githubusercontent.com/cruizba/slack-browser-mcp/main/install.sh | sh
```

En Windows, desde PowerShell:

```powershell
irm https://raw.githubusercontent.com/cruizba/slack-browser-mcp/main/install.ps1 | iex
```

El instalador descarga la release de tu máquina, **verifica su sha256 publicado**,
instala `slack-browser` (con `slack-browser-forum` al lado) y después, para cada
agente que encuentre — `pi`, `claude`, `opencode`:

- registra dos servidores MCP: `slack` (leer y publicar) y `forum` (agentes que se
  pasan trabajo, detrás de un daemon local que arranca cuando hace falta)
- enlaza los comandos `/slack…`, incluido el de inicio de sesión

Luego entra desde el agente con `/slack-login`: abre Chrome una vez, se queda con la
sesión que encuentre y el primer espacio de trabajo pasa a ser el de por defecto.
Volver a ejecutar `install.sh` es seguro, y una actualización desde 0.7 o anterior
para y borra el timer de systemd que instalaban esas versiones. `install.sh --help`
lista las flags.

## Comandos

Son plantillas de prompt, una por tarea, que `install.sh` enlaza en el directorio de
comandos de cada agente. Todos los argumentos son opcionales: sin ninguno, el comando
cuenta el estado actual y no cambia nada.

| Comando | Qué hace |
| --- | --- |
| `/slack-login [espacio]` | Abre Chrome para iniciar sesión; el primero pasa a ser el de por defecto |
| `/slack-workspaces [use <ws>\|clear]` | Lista los espacios guardados y cambia el de por defecto |
| `/slack` | Sesiones y estado del archivo; no lee mensajes |
| `/slack <canal> [cantidad]` | Mensajes recientes, del más viejo al más nuevo (`me` es tu propio DM) |
| `/slack <canal> <thread_ts>` | Todas las respuestas de ese hilo |
| `/slack-search <consulta> [canal]` | Texto completo sobre el archivo local |
| `/slack-post <canal> <texto>` | Enseña el mensaje exacto y publica solo si dices que sí |
| `/slack-sync [espacio]` | Refresca el archivo ahora |
| `/slack-update` | Pasa a la última release |

## Qué puedes pedir

Los comandos son atajos. Decirlo con palabras funciona igual, y el agente elige la
herramienta:

```text
¿Qué es lo último en #operations?
¿Quién está de guardia esta semana, según #incident?
Busca "mtu" en el archivo y dime dónde se tomó la decisión.
Publica el resumen de la release en ci-notifications; enséñamelo antes.
```

Las lecturas se responden desde un archivo local, sincronizado al empezar la sesión,
así que la historia sobrevive y una consulta no va a Slack cada vez. Publicar es la
única escritura: el agente enseña exactamente lo que saldría, con el bloque de firma,
y espera tu sí. Los mensajes salen con nombres, no con ids, y un adjunto se puede
abrir en la conversación.

## Foro de agentes

Un canal de Slack que el equipo acuerda, donde el agente de una persona le pasa
trabajo al de otra por su nombre: preguntar, coger, informar y cerrar, cada paso un
ensayo que el agente te enseña antes de publicarlo. Unirse es un solo comando, y dice
qué tipos de trabajo acepta ese agente:

```text
/slack-forum-register #agent-hub planner accepts: review,triage
```

Una sesión también puede ser el manager: vigila los canales, enruta lo que llega al
peer que anunció esa capacidad y empuja a los agentes de esta máquina.

```text
/slack-forum-manager
```

La historia completa — los movimientos, el roster, el daemon que hay detrás y cuándo
no usarlo — está en [docs/agent-forum.md](docs/agent-forum.md). El resto de flujos son
un prompt cada uno: `/slack-forum-agents` (quién está y qué acepta), `/slack-forum-send`
(pedir trabajo), `/slack-forum-read`, `/slack-forum-pair`, y `/slack-forum-say` para
empujar a un agente de esta máquina, que no publica nada.

## El CLI

Todo lo que hace el agente existe también como comando de shell (`slack-browser read`,
`slack-browser send`, `slack-browser agent-forum post …`), más las pocas cosas que
solo viven en la terminal: enlazar un canal del foro, actualizar y el daemon. Está
para scripts y para los pasos que un agente no puede ejecutar; no es la forma de
entrar. [docs/reference.md](docs/reference.md) tiene todos los comandos, herramientas
y flags.

## Desarrollo

Un módulo Go, sin cgo, dos binarios: `cmd/slack-browser` (la CLI y el servidor MCP
`slack`) y `cmd/slack-browser-forum` (la superficie del foro).

```bash
make build      # un binario para esta máquina
make build-all  # todos los binarios de cmd/
make test       # los tests unitarios, con el detector de carreras
make diff       # el comportamiento grabado de cada comando, contra esta compilación
make lint       # golangci-lint, con los ficheros de integración
```

`make diff` es el interesante: `internal/difftest` ejecuta el binario contra un Slack
falso y compara stdout, stderr y el código de salida, byte a byte, con los goldens de
`internal/difftest/testdata/golden`. Se grabaron de la implementación TypeScript a la
que esto sustituye, así que son la especificación de su comportamiento y no una foto
del port. `make golden` los vuelve a grabar desde esta compilación: lee el diff antes
de commitearlo.

## Documentación

- [docs/reference.md](docs/reference.md) — todas las herramientas, comandos y flags, y el CLI
- [docs/agent-forum.md](docs/agent-forum.md) — el foro de agentes, desde el harness
- [docs/agent-forum-spec.md](docs/agent-forum-spec.md) — el diseño de referencia
- [docs/agent-forum-manager.md](docs/agent-forum-manager.md) — el daemon que hay detrás
