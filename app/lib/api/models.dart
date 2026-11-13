/// Hand-written models of docs/api/openapi.yaml (frontend-app.md section 7).
///
/// Parsing is tolerant: unknown fields are ignored, unknown enum values become
/// the enum's `unknown` member (the raw string is kept next to it where the UI
/// may want to show it), missing fields take defaults.
library;

import 'package:meta/meta.dart';

import 'json.dart';

// ---------------------------------------------------------------- enums

enum Scope {
  read('read'),
  write('write'),
  admin('admin'),
  unknown('');

  const Scope(this.wire);
  final String wire;
  static Scope parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum Status {
  starting('starting'),
  working('working'),
  idle('idle'),
  waiting('waiting'),
  exited('exited'),
  unknown('');

  const Status(this.wire);
  final String wire;
  static Status parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum SessionState {
  starting('starting'),
  running('running'),
  exited('exited'),
  unknown('');

  const SessionState(this.wire);
  final String wire;
  static SessionState parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum InputState {
  empty('empty'),
  draft('draft'),
  suggestion('suggestion'),
  unknown('');

  const InputState(this.wire);
  final String wire;
  static InputState parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum LineRole {
  transcript('transcript'),
  status('status'),
  dialog('dialog'),
  gap('gap'),
  unknown('');

  const LineRole(this.wire);
  final String wire;
  static LineRole parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum OptionKind {
  numbered('numbered'),
  pointer('pointer'),
  checkbox('checkbox'),
  yesno('yesno'),
  unknown('');

  const OptionKind(this.wire);
  final String wire;
  static OptionKind parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum AttentionReason {
  turnDone('turn_done'),
  waiting('waiting'),
  bell('bell'),
  notify('notify'),
  unknown('');

  const AttentionReason(this.wire);
  final String wire;
  static AttentionReason parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum ActAction {
  prompt('prompt'),
  choose('choose'),
  keys('keys'),
  interrupt('interrupt'),
  cancel('cancel'),
  unknown('');

  const ActAction(this.wire);
  final String wire;
  static ActAction parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum ActOutcome {
  submitted('submitted'),
  pasted('pasted'),
  selected('selected'),
  keysSent('keys_sent'),
  interrupted('interrupted'),
  cancelled('cancelled'),
  unknown('');

  const ActOutcome(this.wire);
  final String wire;
  static ActOutcome parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

enum MessageState {
  queued('queued'),
  delivered('delivered'),
  read('read'),
  replied('replied'),
  unknown('');

  const MessageState(this.wire);
  final String wire;
  static MessageState parse(Object? raw) =>
      parseEnum(values, raw, unknown, (e) => e.wire);
}

// ---------------------------------------------------------------- info

@immutable
class DaemonInfo {
  const DaemonInfo({this.version = '', this.pid = 0, this.epoch = ''});
  factory DaemonInfo.fromJson(Json j) => DaemonInfo(
    version: jString(j['version']),
    pid: jInt(j['pid']),
    epoch: jString(j['epoch']),
  );
  final String version;
  final int pid;
  final String epoch;
}

@immutable
class ProfileInfo {
  const ProfileInfo({required this.name, this.builtin = false});
  factory ProfileInfo.fromJson(Json j) =>
      ProfileInfo(name: jString(j['name']), builtin: jBool(j['builtin']));
  final String name;
  final bool builtin;
}

@immutable
class AliasInfo {
  const AliasInfo({required this.name, this.dir = '', this.source = ''});
  factory AliasInfo.fromJson(Json j) => AliasInfo(
    name: jString(j['name']),
    dir: jString(j['dir']),
    source: jString(j['source']),
  );
  final String name;
  final String dir;

  /// `file` or `env` (kept as text: purely informational).
  final String source;
}

@immutable
class Limits {
  const Limits({this.maxBodyKb, this.promptMaxChars});
  factory Limits.fromJson(Json j) => Limits(
    maxBodyKb: jIntOrNull(j['max_body_kb']),
    promptMaxChars: jIntOrNull(j['prompt_max_chars']),
  );
  final int? maxBodyKb;
  final int? promptMaxChars;
}

/// `GET /v1/info`.
@immutable
class Info {
  const Info({
    required this.host,
    this.gatewayVersion = '',
    this.daemon = const DaemonInfo(),
    this.identity = '',
    this.scopes = const [],
    this.harnesses = const [],
    this.profiles = const [],
    this.aliases = const [],
    this.limits = const Limits(),
    this.apiVersion,
    this.features = const [],
  });

  factory Info.fromJson(Json j) => Info(
    host: jString(j['host']),
    gatewayVersion: jString(asJson(j['gateway'])['version']),
    daemon: DaemonInfo.fromJson(asJson(j['daemon'])),
    identity: jString(j['identity']),
    scopes: [for (final s in jStrings(j['scopes'])) Scope.parse(s)],
    harnesses: jStrings(j['harnesses']),
    profiles: jList(j['profiles'], ProfileInfo.fromJson),
    aliases: jList(j['aliases'], AliasInfo.fromJson),
    limits: Limits.fromJson(asJson(j['limits'])),
    apiVersion: jStringOrNull(j['api_version']),
    features: jStrings(j['features']),
  );

  final String host;
  final String gatewayVersion;
  final DaemonInfo daemon;
  final String identity;
  final List<Scope> scopes;
  final List<String> harnesses;
  final List<ProfileInfo> profiles;
  final List<AliasInfo> aliases;
  final Limits limits;

  /// ADR-A16: absent on gateways older than the version negotiation.
  final String? apiVersion;
  final List<String> features;

  bool hasFeature(String f) => features.contains(f);
  bool get canWrite => scopes.contains(Scope.write) || canAdmin;
  bool get canAdmin => scopes.contains(Scope.admin);
}

// ---------------------------------------------------------------- sessions

@immutable
class SessionSummary {
  const SessionSummary({
    required this.name,
    this.state = SessionState.unknown,
    this.stateRaw = '',
    this.pid,
    this.cwd = '',
    this.cmd = const [],
    this.profile = '',
    this.exitCode,
    this.startedAt,
    this.lastOutput,
    this.idleMs = 0,
    this.unread = 0,
    this.attached = 0,
    this.cols = 0,
    this.rows = 0,
    this.title,
    this.inputState,
    this.status,
    this.statusRaw,
    this.attention,
    this.revision,
    this.preview,
  });

  factory SessionSummary.fromJson(Json j) => SessionSummary(
    name: jString(j['name']),
    state: SessionState.parse(j['state']),
    stateRaw: jString(j['state']),
    pid: jIntOrNull(j['pid']),
    cwd: jString(j['cwd']),
    cmd: jStrings(j['cmd']),
    profile: jString(j['profile']),
    exitCode: jIntOrNull(j['exit_code']),
    startedAt: jTime(j['started_at']),
    lastOutput: jTime(j['last_output']),
    idleMs: jInt(j['idle_ms']),
    unread: jInt(j['unread']),
    attached: jInt(j['attached']),
    cols: jInt(j['cols']),
    rows: jInt(j['rows']),
    title: jStringOrNull(j['title']),
    inputState: j['input_state'] == null
        ? null
        : InputState.parse(j['input_state']),
    status: j['status'] == null ? null : Status.parse(j['status']),
    statusRaw: jStringOrNull(j['status']),
    attention: jBoolOrNull(j['attention']),
    revision: jIntOrNull(j['revision']),
    preview: jStringOrNull(j['preview']),
  );

  final String name;
  final SessionState state;
  final String stateRaw;
  final int? pid;
  final String cwd;
  final List<String> cmd;
  final String profile;
  final int? exitCode;
  final DateTime? startedAt;
  final DateTime? lastOutput;
  final int idleMs;
  final int unread;
  final int attached;
  final int cols;
  final int rows;
  final String? title;
  final InputState? inputState;

  /// Semantic status; absent when the daemon could not compute one.
  final Status? status;
  final String? statusRaw;
  final bool? attention;
  final int? revision;
  final String? preview;
}

/// Result of `GET /v1/sessions`.
@immutable
class SessionList {
  const SessionList({
    required this.sessions,
    this.etag,
    this.notModified = false,
  });
  final List<SessionSummary> sessions;
  final String? etag;

  /// True for a `304`: [sessions] is empty and the caller keeps its copy.
  final bool notModified;
}

/// Body of `POST /v1/sessions`. `cmd`, `args` and `env` need the admin scope.
@immutable
class RunRequest {
  const RunRequest({
    required this.target,
    this.harness,
    this.name,
    this.profile,
    this.args,
    this.cmd,
    this.env,
  });
  final String target;
  final String? harness;
  final String? name;
  final String? profile;
  final List<String>? args;
  final List<String>? cmd;
  final Map<String, String>? env;

  Json toJson() => compact({
    'target': target,
    'harness': harness,
    'name': name,
    'profile': profile,
    'args': args,
    'cmd': cmd,
    'env': env,
  });
}

// ---------------------------------------------------------------- view

@immutable
class Line {
  const Line({
    required this.text,
    this.role = LineRole.transcript,
    String? roleRaw,
  }) : roleRaw = roleRaw ?? '';
  factory Line.fromJson(Json j) => Line(
    text: jString(j['text']),
    role: LineRole.parse(j['role']),
    roleRaw: jString(j['role']),
  );
  final String text;
  final LineRole role;
  final String roleRaw;

  Json toJson() =>
      compact({'text': text, 'role': roleRaw.isEmpty ? null : roleRaw});
}

@immutable
class Input {
  const Input({
    this.text = '',
    this.ghost = '',
    this.state = InputState.empty,
    this.stateRaw = 'empty',
  });
  factory Input.fromJson(Json j) => Input(
    text: jString(j['text']),
    ghost: jString(j['ghost']),
    state: j['state'] == null ? InputState.empty : InputState.parse(j['state']),
    stateRaw: jString(j['state'], 'empty'),
  );
  static const empty = Input();
  final String text;
  final String ghost;
  final InputState state;
  final String stateRaw;

  Json toJson() => {'text': text, 'ghost': ghost, 'state': stateRaw};
}

@immutable
class Option {
  const Option({
    required this.id,
    required this.label,
    this.index = 0,
    this.kind = OptionKind.unknown,
    this.kindRaw = '',
    this.selected = false,
    this.checked,
    this.confidence = 0,
  });
  factory Option.fromJson(Json j) => Option(
    id: jString(j['id']),
    label: jString(j['label']),
    index: jInt(j['index']),
    kind: OptionKind.parse(j['kind']),
    kindRaw: jString(j['kind']),
    selected: jBool(j['selected']),
    checked: jBoolOrNull(j['checked']),
    confidence: jDouble(j['confidence']),
  );
  final String id;
  final String label;
  final int index;
  final OptionKind kind;
  final String kindRaw;
  final bool selected;
  final bool? checked;

  /// 0..1; low values are shown as "suggested" (frontend-app.md section 1.2).
  final double confidence;

  Json toJson() => compact({
    'id': id,
    'label': label,
    'index': index,
    'kind': kindRaw,
    'selected': selected,
    'checked': checked,
    'confidence': confidence,
  });
}

@immutable
class Hint {
  const Hint({this.key = '', this.action = ''});
  factory Hint.fromJson(Json j) =>
      Hint(key: jString(j['key']), action: jString(j['action']));
  final String key;
  final String action;
  Json toJson() => {'key': key, 'action': action};
}

@immutable
class Attention {
  const Attention({
    this.set = false,
    this.reason = AttentionReason.unknown,
    this.reasonRaw,
    this.since,
    this.sinceRaw,
  });
  factory Attention.fromJson(Json j) => Attention(
    set: jBool(j['set']),
    reason: AttentionReason.parse(j['reason']),
    reasonRaw: jStringOrNull(j['reason']),
    since: jTime(j['since']),
    sinceRaw: jStringOrNull(j['since']),
  );
  static const none = Attention();
  final bool set;
  final AttentionReason reason;
  final String? reasonRaw;
  final DateTime? since;
  final String? sinceRaw;

  Json toJson() =>
      compact({'set': set, 'reason': reasonRaw, 'since': sinceRaw});
}

@immutable
class Dialog {
  const Dialog({this.question = ''});
  factory Dialog.fromJson(Json j) => Dialog(question: jString(j['question']));
  final String question;
  Json toJson() => {'question': question};
}

/// Semantic view (L1) of a session.
@immutable
class View {
  const View({
    required this.name,
    required this.epoch,
    required this.revision,
    this.status = Status.unknown,
    this.statusRaw = '',
    this.statusReason,
    this.attention = Attention.none,
    this.title,
    this.cwd,
    this.firstLine = 0,
    this.lines = const [],
    this.input = Input.empty,
    this.options = const [],
    this.hints = const [],
    this.dialog,
    this.idleMs = 0,
    this.exitCode,
  });

  factory View.fromJson(Json j) => View(
    name: jString(j['name']),
    epoch: jString(j['epoch']),
    revision: jInt(j['revision']),
    status: Status.parse(j['status']),
    statusRaw: jString(j['status']),
    statusReason: jStringOrNull(j['status_reason']),
    attention: Attention.fromJson(asJson(j['attention'])),
    title: jStringOrNull(j['title']),
    cwd: jStringOrNull(j['cwd']),
    firstLine: jInt(j['first_line']),
    lines: jList(j['lines'], Line.fromJson),
    input: Input.fromJson(asJson(j['input'])),
    options: jList(j['options'], Option.fromJson),
    hints: jList(j['hints'], Hint.fromJson),
    dialog: j['dialog'] is Map ? Dialog.fromJson(asJson(j['dialog'])) : null,
    idleMs: jInt(j['idle_ms']),
    exitCode: jIntOrNull(j['exit_code']),
  );

  final String name;
  final String epoch;
  final int revision;
  final Status status;
  final String statusRaw;
  final String? statusReason;
  final Attention attention;
  final String? title;
  final String? cwd;

  /// Absolute index of `lines[0]` (lines are tail-truncated).
  final int firstLine;
  final List<Line> lines;
  final Input input;
  final List<Option> options;
  final List<Hint> hints;
  final Dialog? dialog;
  final int idleMs;
  final int? exitCode;

  /// Absolute index one past the last line.
  int get endLine => firstLine + lines.length;

  /// Canonical JSON of the known fields. Two views with the same content have
  /// equal `toJson()` output, which is how patch application is checked
  /// against a fresh view.
  Json toJson() => compact({
    'name': name,
    'epoch': epoch,
    'revision': revision,
    'status': statusRaw,
    'status_reason': statusReason,
    'attention': attention.toJson(),
    'title': title,
    'cwd': cwd,
    'first_line': firstLine,
    'lines': [for (final l in lines) l.toJson()],
    'input': input.toJson(),
    'options': [for (final o in options) o.toJson()],
    'hints': [for (final h in hints) h.toJson()],
    'dialog': dialog?.toJson(),
    'idle_ms': idleMs,
    'exit_code': exitCode,
  });

  View copyWithLines({
    required int firstLine,
    required List<Line> lines,
    required int revision,
  }) => View.fromJson({
    ...toJson(),
    'first_line': firstLine,
    'revision': revision,
    'lines': [for (final l in lines) l.toJson()],
  });
}

/// `data` of SSE `view.patch`: see [ViewState.applyPatch] for the semantics.
@immutable
class ViewStreamPatch {
  const ViewStreamPatch({
    required this.fromRevision,
    required this.revision,
    required this.epoch,
    required this.firstLine,
    this.spliceAt,
    this.spliceDelete = 0,
    this.spliceInsert = const [],
    this.set = const {},
  });

  factory ViewStreamPatch.fromJson(Json j) {
    final s = j['splice'];
    final sp = s is Map ? asJson(s) : null;
    return ViewStreamPatch(
      fromRevision: jInt(j['from_revision']),
      revision: jInt(j['revision']),
      epoch: jString(j['epoch']),
      firstLine: jInt(j['first_line']),
      spliceAt: sp == null ? null : jInt(sp['at']),
      spliceDelete: sp == null ? 0 : jInt(sp['delete']),
      spliceInsert: sp == null ? const [] : jList(sp['insert'], Line.fromJson),
      set: asJson(j['set']),
    );
  }

  final int fromRevision;
  final int revision;
  final String epoch;
  final int firstLine;

  /// Relative index (after dropping lines below [firstLine]); null = no splice.
  final int? spliceAt;
  final int spliceDelete;
  final List<Line> spliceInsert;

  /// Replaced scalar fields; a `null` value clears `dialog` / `exit_code`.
  final Json set;

  bool get hasSplice => spliceAt != null;
}

/// Answer of `GET .../view`: a full view, a `304`/`unchanged` marker or (for
/// forward compatibility) a server-computed patch.
sealed class ViewResult {
  const ViewResult();
}

class ViewFull extends ViewResult {
  const ViewFull(this.view, {this.etag});
  final View view;
  final String? etag;
}

class ViewNotModified extends ViewResult {
  const ViewNotModified({this.etag, this.revision, this.epoch});
  final String? etag;
  final int? revision;
  final String? epoch;
}

/// `{patch: true, base, revision, ...}`; the client never asks for these
/// (no `since`), so the raw body is kept for callers that do.
class ViewPatched extends ViewResult {
  const ViewPatched({
    required this.base,
    required this.revision,
    required this.raw,
    this.etag,
  });
  final int base;
  final int revision;
  final Json raw;
  final String? etag;
}

// ---------------------------------------------------------------- act

@immutable
class ActRequest {
  const ActRequest._(
    this.action, {
    this.text,
    this.submit,
    this.optionId,
    this.label,
    this.keys,
    this.revision,
    this.epoch,
    this.force,
  });

  /// Type [text] into the session; [submit] defaults to true on the server.
  factory ActRequest.prompt(
    String text, {
    bool? submit,
    int? revision,
    String? epoch,
    bool? force,
  }) => ActRequest._(
    ActAction.prompt,
    text: text,
    submit: submit,
    revision: revision,
    epoch: epoch,
    force: force,
  );

  /// Pick option [optionId] seen as [label] on view [revision] of [epoch].
  factory ActRequest.choose(
    String optionId, {
    required int revision,
    required String epoch,
    String? label,
    bool? force,
  }) => ActRequest._(
    ActAction.choose,
    optionId: optionId,
    label: label,
    revision: revision,
    epoch: epoch,
    force: force,
  );

  factory ActRequest.keys(List<String> keys, {int? revision, String? epoch}) =>
      ActRequest._(
        ActAction.keys,
        keys: keys,
        revision: revision,
        epoch: epoch,
      );

  factory ActRequest.interrupt({int? revision, String? epoch}) =>
      ActRequest._(ActAction.interrupt, revision: revision, epoch: epoch);
  factory ActRequest.cancel({int? revision, String? epoch}) =>
      ActRequest._(ActAction.cancel, revision: revision, epoch: epoch);

  final ActAction action;
  final String? text;
  final bool? submit;
  final String? optionId;
  final String? label;
  final List<String>? keys;
  final int? revision;
  final String? epoch;
  final bool? force;

  Json toJson() => compact({
    'action': action.wire,
    'revision': revision,
    'epoch': epoch,
    'text': text,
    'submit': submit,
    'option_id': optionId,
    'label': label,
    'force': force,
    'keys': keys,
  });
}

@immutable
class ActResult {
  const ActResult({
    required this.action,
    this.result = ActOutcome.unknown,
    this.resultRaw = '',
    this.submitted,
    this.revision = 0,
    this.status = Status.unknown,
    this.warning,
    this.replayed = false,
  });
  factory ActResult.fromJson(Json j, {bool replayed = false}) => ActResult(
    action: jString(j['action']),
    result: ActOutcome.parse(j['result']),
    resultRaw: jString(j['result']),
    submitted: jBoolOrNull(j['submitted']),
    revision: jInt(j['revision']),
    status: Status.parse(j['status']),
    warning: jStringOrNull(j['warning']),
    replayed: replayed,
  );
  final String action;
  final ActOutcome result;
  final String resultRaw;
  final bool? submitted;
  final int revision;
  final Status status;
  final String? warning;

  /// `Idempotent-Replayed: true` was present: a stored answer, not a new run.
  final bool replayed;
}

@immutable
class TerminalSnapshot {
  const TerminalSnapshot({
    this.format = 'text',
    this.text = '',
    this.ghost,
    this.ghostHidden,
    this.cols = 0,
    this.rows = 0,
  });
  factory TerminalSnapshot.fromJson(Json j) => TerminalSnapshot(
    format: jString(j['format'], 'text'),
    text: jString(j['text']),
    ghost: jStringOrNull(j['ghost']),
    ghostHidden: jBoolOrNull(j['ghost_hidden']),
    cols: jInt(j['cols']),
    rows: jInt(j['rows']),
  );
  final String format;
  final String text;
  final String? ghost;
  final bool? ghostHidden;
  final int cols;
  final int rows;
}

// ---------------------------------------------------------------- mailbox

@immutable
class Message {
  const Message({
    required this.id,
    this.sender = '',
    this.recipient = '',
    this.subject = '',
    this.body = '',
    this.replyTo,
    this.state = MessageState.unknown,
    this.stateRaw = '',
    this.attempts = 0,
    this.createdAt,
    this.deliveredAt,
    this.readAt,
    this.repliedAt,
    this.warning,
  });
  factory Message.fromJson(Json j) => Message(
    id: jInt(j['id']),
    sender: jString(j['sender']),
    recipient: jString(j['recipient']),
    subject: jString(j['subject']),
    body: jString(j['body']),
    replyTo: jIntOrNull(j['reply_to']),
    state: MessageState.parse(j['state']),
    stateRaw: jString(j['state']),
    attempts: jInt(j['attempts']),
    createdAt: jTime(j['created_at']),
    deliveredAt: jTime(j['delivered_at']),
    readAt: jTime(j['read_at']),
    repliedAt: jTime(j['replied_at']),
    warning: jStringOrNull(j['warning']),
  );
  final int id;
  final String sender;
  final String recipient;
  final String subject;
  final String body;
  final int? replyTo;
  final MessageState state;
  final String stateRaw;
  final int attempts;
  final DateTime? createdAt;
  final DateTime? deliveredAt;
  final DateTime? readAt;
  final DateTime? repliedAt;
  final String? warning;
}

@immutable
class MessagePage {
  const MessagePage({required this.messages, this.nextBefore});
  final List<Message> messages;

  /// Cursor for the next page (`before`); null on the last page.
  final int? nextBefore;
}

@immutable
class SendRequest {
  const SendRequest({
    required this.to,
    required this.body,
    this.subject,
    this.replyTo,
  });
  final String to;
  final String body;
  final String? subject;
  final int? replyTo;
  Json toJson() => compact({
    'to': to,
    'subject': subject,
    'body': body,
    'reply_to': replyTo,
  });
}

// ---------------------------------------------------------------- pairing

/// Answer of `POST /v1/pair`. [token] is shown by the host only once; keep it
/// in the secret store, never in logs ([toString] redacts it).
@immutable
class PairResult {
  const PairResult({
    required this.token,
    required this.deviceId,
    this.scope = Scope.unknown,
  });
  factory PairResult.fromJson(Json j) => PairResult(
    token: jString(j['token']),
    deviceId: jString(j['device_id']),
    scope: Scope.parse(j['scope']),
  );
  final String token;
  final String deviceId;
  final Scope scope;

  @override
  String toString() =>
      'PairResult(deviceId: $deviceId, scope: ${scope.wire}, token: ***)';
}

// ---------------------------------------------------------------- events

/// SSE `data` payload of `/v1/events` and of the non-view events of a view
/// stream. `type` repeats the SSE event name; `data` is event specific.
@immutable
class GatewayEvent {
  const GatewayEvent({
    required this.type,
    this.session,
    this.ts,
    this.data = const {},
  });
  factory GatewayEvent.fromJson(Json j) => GatewayEvent(
    type: jString(j['type']),
    session: jStringOrNull(j['session']),
    ts: jTime(j['ts']),
    data: asJson(j['data']),
  );
  final String type;
  final String? session;
  final DateTime? ts;
  final Json data;

  /// Event-specific helpers (daemon-ops.md section 6), all tolerant.
  Status? get toStatus => data['to'] == null ? null : Status.parse(data['to']);
  int? get revision => jIntOrNull(data['revision']);
}
