import 'dart:async';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/providers.dart';
import 'package:flutter_riverpod/misc.dart' show Override;

/// Scriptable client; unimplemented calls fail loudly.
class FakeClient implements AgentdClient {
  Info infoResult = const Info(
    host: 'box',
    scopes: [Scope.read, Scope.write],
    identity: 'human:jose',
    harnesses: ['claude', 'codex'],
    aliases: [
      AliasInfo(name: 'webapp', dir: '/home/jose/src/webapp'),
      AliasInfo(name: 'api', dir: '/srv/api'),
    ],
  );
  Object? runError;
  final runCalls = <({RunRequest req, String? key})>[];
  List<SessionSummary> sessions = [];
  final killCalls = <(String, String?)>[];
  final removeCalls = <(String, bool)>[];
  Object? killError;
  Object? removeError;

  /// Pages by `before` cursor (null = first page).
  Map<int?, MessagePage> pages = {};
  final listCalls = <({int? before, String? mailbox, int? limit})>[];
  final replyCalls = <({int id, String body, String? key})>[];
  final sendCalls = <({SendRequest req, String? key})>[];
  final readCalls = <int>[];
  Object? replyError;
  Object? sendError;
  Map<int, Message> messages = {};

  @override
  Future<Info> info() async => infoResult;

  @override
  Future<SessionSummary> runSession(
    RunRequest req, {
    String? idempotencyKey,
  }) async {
    runCalls.add((req: req, key: idempotencyKey));
    if (runError != null) throw runError!;
    return SessionSummary(
      name: req.name ?? '${req.harness}@${req.target}',
      state: SessionState.starting,
    );
  }

  @override
  Future<SessionList> listSessions({String? ifNoneMatch}) async =>
      SessionList(sessions: sessions);

  @override
  Future<SessionSummary> killSession(String name, {String? signal}) async {
    killCalls.add((name, signal));
    if (killError != null) throw killError!;
    return SessionSummary(name: name);
  }

  @override
  Future<void> removeSession(String name, {bool force = false}) async {
    removeCalls.add((name, force));
    if (removeError != null) throw removeError!;
  }

  @override
  Future<MessagePage> listMailbox({
    bool all = false,
    String? mailbox,
    int? limit,
    int? before,
  }) async {
    listCalls.add((before: before, mailbox: mailbox, limit: limit));
    return pages[before] ?? const MessagePage(messages: []);
  }

  @override
  Future<Message> getMessage(int id, {String? mailbox}) async =>
      messages[id] ?? Message(id: id, body: 'body $id', sender: 's');

  @override
  Future<Message> markMessageRead(int id) async {
    readCalls.add(id);
    return Message(id: id, state: MessageState.read, stateRaw: 'read');
  }

  @override
  Future<Message> replyMessage(
    int id,
    String body, {
    String? idempotencyKey,
  }) async {
    replyCalls.add((id: id, body: body, key: idempotencyKey));
    if (replyError != null) throw replyError!;
    return Message(id: 900 + id, body: body);
  }

  @override
  Future<Message> sendMessage(SendRequest req, {String? idempotencyKey}) async {
    sendCalls.add((req: req, key: idempotencyKey));
    if (sendError != null) throw sendError!;
    return Message(id: 500, body: req.body, recipient: req.to);
  }

  @override
  dynamic noSuchMethod(Invocation i) =>
      throw UnimplementedError('${i.memberName}');
}

Message msg(
  int id, {
  DateTime? at,
  MessageState state = MessageState.delivered,
  String sender = 'codex@webapp',
  String subject = '',
}) => Message(
  id: id,
  sender: sender,
  recipient: 'human:jose',
  subject: subject,
  body: 'body $id',
  state: state,
  stateRaw: state.wire,
  createdAt: at ?? DateTime.utc(2026, 11, 13, 10, id),
);

ApiException apiErr(int status, String code, [String message = '']) =>
    ApiException(statusCode: status, code: code, message: message);

/// Overrides every host id to the same/own fake client.
Override clients(Map<String, FakeClient> byHost) =>
    hostClientProvider.overrideWith((ref, id) async => byHost[id]!);
