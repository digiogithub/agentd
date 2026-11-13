/// Kill / remove of sessions as plain functions over a host's client, so any
/// screen (session view header, agents list) can call them. See README.md.
library;

import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/agentd_api.dart';
import 'providers.dart';

sealed class ActionResult {
  const ActionResult();
}

class ActionOk extends ActionResult {
  const ActionOk([this.session]);

  /// The session after a kill (the exit itself arrives on `/v1/events`).
  final SessionSummary? session;
}

class ActionFailed extends ActionResult {
  const ActionFailed(this.message, {this.notFound = false});
  final String message;

  /// The session is already gone (treat as done for remove).
  final bool notFound;
}

String describeActionError(Object e, String verb) {
  if (e is ApiException) {
    if (e.isOutcomeUnknown) {
      return 'No answer from the host. The session may have changed: '
          'refresh the list before trying to $verb it again.';
    }
    if (e.isForbidden) return 'This device is not allowed to $verb sessions.';
    if (e.isUnauthenticated) return 'Not authorized: re-pair the host.';
    if (e.isNotFound) return 'The session no longer exists.';
    if (e.code == 'still_running') {
      return 'The session is still running. Kill it first or remove with force.';
    }
    if (e.code == 'exited') return 'The session has already exited.';
    return e.message.isEmpty ? 'Could not $verb (${e.code}).' : e.message;
  }
  if (e is ConnectionException) return 'Cannot reach the host.';
  if (e is PinMismatchException) return 'Host certificate changed: re-pair.';
  return 'Could not $verb: $e';
}

class SessionActions {
  SessionActions(this._ref);
  final Ref _ref;

  Future<AgentdClient> _client(String hostId) =>
      _ref.read(hostClientProvider(hostId).future);

  /// `POST /v1/sessions/{name}/kill`; [signal] `TERM` (default) or `KILL`.
  Future<ActionResult> kill(
    String hostId,
    String name, {
    String signal = 'TERM',
  }) async {
    try {
      final s = await (await _client(hostId)).killSession(name, signal: signal);
      return ActionOk(s);
    } on Object catch (e) {
      return ActionFailed(
        describeActionError(e, 'kill'),
        notFound: e is ApiException && e.isNotFound,
      );
    }
  }

  /// `DELETE /v1/sessions/{name}`; [force] is required while it is running.
  Future<ActionResult> remove(
    String hostId,
    String name, {
    bool force = false,
  }) async {
    try {
      await (await _client(hostId)).removeSession(name, force: force);
      return const ActionOk();
    } on Object catch (e) {
      return ActionFailed(
        describeActionError(e, 'remove'),
        notFound: e is ApiException && e.isNotFound,
      );
    }
  }
}

final sessionActionsProvider = Provider<SessionActions>(SessionActions.new);
