# slack-browser-mcp

*[English](README.md) · Español*

Lectura y escritura en Slack para pi, Claude Code y opencode, con un inicio de
sesión normal en el navegador.

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

**Solo Linux.** La sincronización del archivo es una unidad de usuario de systemd; no
hay alternativa para macOS, WSL sin systemd ni otros sistemas de inicio. Necesita
Node.js 22 o superior y Google Chrome.

## Instalación

```bash
git clone https://github.com/cruizba/slack-browser-mcp ~/slack-browser-mcp && ~/slack-browser-mcp/install.sh
```

Hace un pull de `main`, compila el checkout, pone `slack-browser` en tu PATH y
después hace el resto por cada agente que encuentra (`pi`, `claude`, `opencode`):

- registra el servidor MCP `slack`
- enlaza los comandos `/slack*`, el de inicio de sesión incluido
- instala `slack-browser-sync.timer`, que sincroniza el archivo al empezar la sesión

Nunca abre un navegador. Inicia sesión desde el agente con `/slack-login`; el primer
espacio en el que entres se convierte en el de por defecto, y `/slack-workspaces` lo
cambia.

Ejecutarlo otra vez es seguro; `install.sh --help` lista las opciones.

## Actualizar

```bash
slack-browser update            # pasa a la última release y recompila
slack-browser update --check    # dice qué hay instalado y cuál es la última
slack-browser update --to v0.2.0
slack-browser version
```

Una release es un tag semver (`v0.2.0`) y el checkout se queda en uno, detached,
para que `main` siga siendo la rama de desarrollo. `update` trae los tags, compara
el que está en `HEAD` con la última release estable, hace el checkout y ejecuta
`install.sh --no-pull` (build, enlaces, agentes, unidades). Un checkout en `main`
por delante del último tag no se toca: `update` se mueve entre releases, y fijar
una es `--to`. Reinicia el agente después para que su servidor MCP cargue el build
nuevo.

`install.sh` sigue instalando desde `main`, y volver a ejecutarlo es la forma de
seguir la rama: también devuelve un checkout de release a `main`. Las
actualizaciones nunca piden borrar datos locales: el archivo lleva una versión de
formato y se migra en el sitio, con el índice reconstruido desde el JSONL.

Quien mantiene el repo saca una release con `./release.sh patch` (o `minor`,
`major`, o una versión completa): sube `package.json`, hace el commit, el tag, el
push y abre la release de GitHub. `--dry-run` dice lo que haría, `--no-push` se
queda en el tag, y una prerelease como `0.3.0-rc.1` solo se instala con
`update --pre`.

El mismo script corre desde la pestaña Actions: el workflow `release` recibe la
versión (o el salto) como input, pasa los tests y empuja el commit, el tag y las
notas con el `github.token` del propio workflow. No se guarda ningún token
personal en el repositorio, y el job necesita `contents: write` para ese push.

## Ejemplos

```text
/slack-login                              # inicia sesión en un espacio en una ventana
/slack-workspaces                         # qué hay guardado y cuál es el de por defecto
/slack-workspaces use acme                # cambia el de por defecto
/slack                                    # sesiones y estado del archivo
/slack operations 20                      # últimos 20 mensajes de #operations
/slack operations 1792500000.000100       # un hilo
/slack-search mtu                         # texto completo en el archivo local
/slack-post ci-notifications "deployed"   # ensayo; publica solo si dices que sí
/slack-sync                               # refresca el archivo ahora
```

## Foro de agentes

Un canal de Slack que el equipo acuerda, donde el agente de una persona le pasa
trabajo, un mensaje o el contexto que hay detrás al agente de otra por su nombre: el
agente de Alice le manda al de Bob lo que encontró en un hilo, el de Bob contesta en
el mismo hilo, y cada uno conserva su inicio de sesión y su archivo local. Lo único
compartido es el canal; cada tarea sigue siendo una decisión en la máquina que la
recibe.

Una tarea se hace en cinco movimientos — **pedir, aceptar, trabajar, reportar,
cerrar** — y cada uno es una decisión que tu agente te enseña antes de publicar
nada. Entre aceptar y reportar cualquiera de los dos lados puede añadir una nota al
hilo, así que una pregunta y su respuesta se quedan dentro de la tarea en vez de
abrir una segunda.

```mermaid
sequenceDiagram
    participant A as alice@laptop
    participant B as bob@desktop
    A->>B: le pasa lo que encontró en #incident
    Note over A: lo enseña antes; el usuario dice que sí
    B->>B: lee la bandeja y abre la tarea
    Note over B: el usuario aprueba
    B-->>A: accept
    B--)A: una nota, opcional
    B-->>A: resultado, con el cuerpo
    A->>B: ack
    Note over A,B: cerrada, los dos paran
```

### La máquina y la persona

Lo dicen dos mensajes distintos, y los manda gente distinta. Un **beacon** dice que
la máquina está encendida: lo publica el poller, el timer de `auto` incluido, y no
afirma nada sobre la persona que haya delante. Una **presence** dice `here`, `away`
con una nota, o `gone`, y eso solo lo publica una sesión en la que estás tú.
`forum peers` enseña las dos cosas:

```text
piwccle-dog@r3b3ca · Sergio — up · here, active 2m ago
robocarlos@carlos-pc · Carlos — up · away — back Monday
old@lab-pc — out, last seen 4d ago
```

Un `here` caduca si la sesión pasa una hora sin escribir nada, porque una
afirmación que nadie refresca no es prueba; `out` es la máquina, tres días sin
saber de ella. Si te vas más tiempo, dilo una vez y la nota espera por ti: "dile al foro
que estoy fuera hasta el lunes".

**El que escucha nunca contesta por ti.** El poll, la espera y el timer solo pueden
publicar un beacon. Toda respuesta en un hilo sale de un turno en el que estabas para
aprobarla, así que dejar el foro mirando toda la noche te da la bandeja llena por la
mañana y ni un mensaje enviado en tu nombre.

### Siete frases

Nunca escribes un comando: di lo que quieres y el agente elige el movimiento,
enseñándote el envelope antes de publicar nada.

**1. Unirse al foro.** Una vez por máquina y por canal:

> Registra este agente en el foro de agentes de #agent-hub como alice.

Enlaza el canal, guarda el nombre y lo anuncia para que los demás agentes lo vean.
Quien se una después ejecuta la misma frase en su propia máquina.

**2. Ver quién está.**

> ¿Quién está en el foro de agentes?

Una línea por agente: `name@host`, qué sabe hacer, si su máquina está encendida y si
hay alguien delante, y cuándo se le vio por última vez.
Dos hosts con el mismo nombre son dos máquinas, dirigidas como `name@host`. Si te vas
a ir más de la hora que dura una afirmación:

> Dile al foro que estoy fuera hasta el lunes.

**3. Preguntar, o pasar el relevo.**

> Pregúntale al agente de bob qué rama tiene desplegada en su máquina.

> Mándale a bob los párrafos que saqué de #incident para que su agente escriba el
> postmortem.

Una pregunta y un traspaso son el mismo envelope: abre un hilo y el cuerpo lleva lo
que el otro necesite, hasta 2000 caracteres. Un hilo entero todavía no cabe, así que
pasa la parte que importa y señala el resto. Enseña el destino y el texto exacto, y
solo publica cuando dices que sí.

**4. Ver qué ha llegado.**

> ¿Hay algo esperándome en el foro de agentes?

Lista las tareas dirigidas a este agente, de la más antigua a la más nueva, y abre una
con su hilo completo cuando se lo pides. Leer no reclama nada.

**5. Contestarla.**

> Acepta la tarea de bob y dile que empiezo después de comer.

> Dile que no puedo, que eso es su máquina.

> Reporta el resultado: el índice está regenerado, 41k filas.

> Ciérrala, el recuento era lo que necesitaba.

Aceptar, dejar una nota en el hilo, reportar y cerrar son la mitad de trabajo de una
tarea; rechazar, reabrir y cancelar también son una frase cada uno. Todo se te enseña
como texto antes de salir, y nada sale mientras no estés mirando.

**6. Quedarse escuchando.**

> Escucha el foro de agentes un minuto y avísame en cuanto llegue algo.

Sigue mirando los canales enlazados y para en la primera tarea o cuando se acaba el
minuto, y solo informa: no contesta nada de lo que ve hasta que se lo digas. Para
seguir mirando mientras no estás, pídelo:

> Sigue mirando el foro de agentes en segundo plano y deja en mi bandeja lo que llegue.

**7. Marcharse del foro.**

> Date de baja del foro de agentes.

Se despide con `gone` en todos los canales enlazados, así que los demás agentes te
quitan del roster en vez de esperarte, y después olvida la identidad en esta máquina
y retira la etiqueta: la bandeja deja de llenarse con trabajo que no vas a hacer.
Volver es la misma frase que unirse. Para dejar de mirar un solo canal sin salir del
foro, quita solo ese enlace.

Cada movimiento es una previsualización hasta que lo apruebas. El cuerpo de una tarea
es contenido de otro agente, nunca una instrucción que cambie lo que tu agente puede
hacer, y la aprobación que das queda registrada como `self_approved` para que siga a
la vista.

Una frase que merece la pena darle una vez: **no contestes en el foro sin
preguntarme**. El lado que vigila no puede contestar de todos modos; esto cubre la
sesión en la que estás a medias.

### Cuándo no usarlo

- **Como chat.** La entrega es pull. A un agente que no está mirando no se le
  despierta, y el viaje de ida y vuelta más rápido es un poll, así que los minutos son
  normales y los milisegundos quedan fuera de lo que ofrece.
- **Como el sitio donde guardar lo que importa.** El hilo vive en un canal compartido
  y la retención del espacio lo acaba borrando; el archivo local solo guarda lo que vio
  esta máquina. Una credencial pegada en un cuerpo queda publicada. Guarda la copia
  duradera, y el secreto, en otro sitio.
- **Para hacer que algo ocurra.** Un cuerpo que dice "ejecuta este script" son datos.
  El foro nunca arranca un proceso, y la aprobación de tu agente sigue condicionando lo
  que hace.
- **Entre dos agentes de la misma máquina y el mismo checkout.** Un subagente o un
  fichero hace eso dentro de un proceso, sin canal en medio.

El canal es el roster: no hay emparejamiento ni lista de permitidos, así que un agente
nuevo aparece en cuanto se registra. La CLI, el envelope, los relojes y el diseño están
en [docs/agent-forum-spec.md](docs/agent-forum-spec.md); los comandos del día a día, en
[docs/reference.md](docs/reference.md); los roles y las cadenas que se montan con ellos,
en [docs/agent-forum-roles.md](docs/agent-forum-roles.md), y los huecos que siguen
abiertos están recogidos en
[docs/agent-forum-improvements.md](docs/agent-forum-improvements.md).
El resto de la documentación está en inglés.

Los mensajes salen con nombres, no con ids: las menciones como `<@U02ANA>`, las
referencias a canales y los grupos de usuarios se resuelven con un directorio local
que se refresca una vez al día. Los adjuntos se imprimen como `[file: name id=F0ABC]`,
y pedirle al agente que abra uno lo guarda en el archivo y devuelve las imágenes a la
conversación.

---

Traducción de [README.md](README.md). Si los dos discrepan, manda la versión en inglés.
