/// Human mailbox across hosts (frontend-app.md section 1.2, screen 5).
/// The gateway acts as `human:<user>` on each host. See README.md.
library;

import 'dart:async';
import 'dart:convert';

import 'package:flutter/foundation.dart' show immutable;
import 'package:flutter_riverpod/flutter_riverpod.dart';

import '../api/agentd_api.dart';
import '../repositories/host.dart';
import 'launch.dart';
import 'providers.dart';

/// Messages fetched per host and page (`GET /v1/mailbox` allows up to 200).
final mailboxPageSizeProvider = Provider<int>((ref) => 50);

@immutable
class MailboxItem {
  const MailboxItem(this.hostId, this.message);
  final String hostId;
  final Message message;

  /// Not yet read by the human (`queued` / `delivered`).
  bool get unread =>
      message.state != MessageState.read &&
      message.state != MessageState.replied;
}

@immutable
class MailboxFeedState {
  const MailboxFeedState({
    this.items = const [],
    this.loading = false,
    this.loadingMore = false,
    this.loaded = false,
    this.errors = const {},
    this.cursors = const {},
  });
  final List<MailboxItem> items;
  final bool loading;
  final bool loadingMore;
  final bool loaded;

  /// hostId -> message of the last failed fetch.
  final Map<String, String> errors;

  /// hostId -> `before` cursor of its next page; absent = no more pages.
  final Map<String, int> cursors;

  bool get hasMore => cursors.isNotEmpty;
  int get unreadCount => items.where((i) => i.unread).length;

  MailboxFeedState copyWith({
    List<MailboxItem>? items,
    bool? loading,
    bool? loadingMore,
    bool? loaded,
    Map<String, String>? errors,
    Map<String, int>? cursors,
  }) => MailboxFeedState(
    items: items ?? this.items,
    loading: loading ?? this.loading,
    loadingMore: loadingMore ?? this.loadingMore,
    loaded: loaded ?? this.loaded,
    errors: errors ?? this.errors,
    cursors: cursors ?? this.cursors,
  );
}

int _newestFirst(MailboxItem a, MailboxItem b) {
  final ta = a.message.createdAt;
  final tb = b.message.createdAt;
  if (ta != null && tb != null) {
    final c = tb.compareTo(ta);
    if (c != 0) return c;
  } else if (ta != null) {
    return -1;
  } else if (tb != null) {
    return 1;
  }
  final c = b.message.id.compareTo(a.message.id);
  return c != 0 ? c : a.hostId.compareTo(b.hostId);
}

String describeMailboxError(Object e) {
  if (e is ApiException) {
    if (e.isOutcomeUnknown) {
      return 'No answer from the host. The message may have been stored: '
          'check the mailbox before sending again.';
    }
    if (e.isForbidden) return 'This device is not allowed to do that.';
    if (e.isUnauthenticated) return 'Not authorized: re-pair the host.';
    if (e.isNotFound) {
      return e.code == 'not_found'
          ? 'Not found (unknown recipient or message).'
          : e.message;
    }
    if (e.code == 'payload_too_large') return 'The message is too large.';
    if (e.code == 'idempotency_mismatch') {
      return 'This send was already used with different text. Try again.';
    }
    return e.message.isEmpty ? 'Request failed (${e.code}).' : e.message;
  }
  if (e is ConnectionException) return 'Cannot reach the host.';
  if (e is PinMismatchException) return 'Host certificate changed: re-pair.';
  return '$e';
}

/// Unified inbox: every host's human mailbox merged newest first, paged per
/// host with `next_before`. Listing marks fetched messages `delivered` on the
/// daemon (daemon semantics); `read` is set only explicitly.
class MailboxFeed extends Notifier<MailboxFeedState> {
  @override
  MailboxFeedState build() => const MailboxFeedState();

  Future<List<Host>> _hosts() => ref.read(hostsProvider.future);

  Future<AgentdClient> _client(String id) =>
      ref.read(hostClientProvider(id).future);

  /// Reloads the first page of every host (items of unreachable hosts stay).
  Future<void> refresh() async {
    if (state.loading) return;
    state = state.copyWith(loading: true);
    final List<Host> hosts;
    try {
      hosts = await _hosts();
    } on Object catch (e) {
      // The hosts store is unreadable: nothing to load.
      state = state.copyWith(loading: false, errors: {'': '$e'});
      return;
    }
    final size = ref.read(mailboxPageSizeProvider);
    final errors = <String, String>{};
    final cursors = <String, int>{};
    final fresh = <String, List<Message>>{};
    await Future.wait([
      for (final h in hosts)
        () async {
          try {
            final page = await (await _client(h.id))
                .listMailbox(all: true, limit: size);
            fresh[h.id] = page.messages;
            if (page.nextBefore != null) cursors[h.id] = page.nextBefore!;
          } on Object catch (e) {
            errors[h.id] = describeMailboxError(e);
          }
        }(),
    ]);
    final known = {for (final h in hosts) h.id};
    final items = <MailboxItem>[
      for (final i in state.items)
        if (known.contains(i.hostId) && !fresh.containsKey(i.hostId)) i,
      for (final e in fresh.entries)
        for (final m in e.value) MailboxItem(e.key, m),
    ]..sort(_newestFirst);
    state = MailboxFeedState(
      items: items,
      loaded: true,
      errors: errors,
      cursors: {
        for (final e in state.cursors.entries)
          if (!fresh.containsKey(e.key) && known.contains(e.key))
            e.key: e.value,
        ...cursors,
      },
    );
  }

  /// Fetches the next page of every host that has one.
  Future<void> loadMore() async {
    if (state.loadingMore || state.loading || !state.hasMore) return;
    state = state.copyWith(loadingMore: true);
    final size = ref.read(mailboxPageSizeProvider);
    final cursors = {...state.cursors};
    final errors = {...state.errors};
    final added = <MailboxItem>[];
    await Future.wait([
      for (final e in state.cursors.entries)
        () async {
          try {
            final page = await (await _client(e.key))
                .listMailbox(all: true, limit: size, before: e.value);
            added.addAll([
              for (final m in page.messages) MailboxItem(e.key, m),
            ]);
            if (page.nextBefore != null) {
              cursors[e.key] = page.nextBefore!;
            } else {
              cursors.remove(e.key);
            }
            errors.remove(e.key);
          } on Object catch (err) {
            errors[e.key] = describeMailboxError(err);
          }
        }(),
    ]);
    final seen = {for (final i in state.items) (i.hostId, i.message.id)};
    final items = [
      ...state.items,
      for (final i in added)
        if (!seen.contains((i.hostId, i.message.id))) i,
    ]..sort(_newestFirst);
    state = state.copyWith(
      items: items,
      loadingMore: false,
      cursors: cursors,
      errors: errors,
    );
  }

  /// Replaces a cached message after read / reply / fetch.
  void update(String hostId, Message m) {
    final items = [
      for (final i in state.items)
        if (i.hostId == hostId && i.message.id == m.id)
          MailboxItem(hostId, m)
        else
          i,
    ];
    state = state.copyWith(items: items);
  }
}

final mailboxFeedProvider = NotifierProvider<MailboxFeed, MailboxFeedState>(
  MailboxFeed.new,
);

/// Unread badge: messages not yet marked read, across the loaded pages.
final mailboxUnreadProvider = Provider<int>(
  (ref) => ref.watch(mailboxFeedProvider).unreadCount,
);

typedef MessageKey = ({String hostId, int id});

/// One message (`GET`, never marks it read).
final messageProvider = FutureProvider.autoDispose.family<Message, MessageKey>(
  (ref, key) async =>
      (await ref.watch(hostClientProvider(key.hostId).future))
          .getMessage(key.id),
);

/// Session names of a host: the recipients a human can write to.
final recipientsProvider = FutureProvider.autoDispose
    .family<List<String>, String>((ref, hostId) async {
      final c = await ref.watch(hostClientProvider(hostId).future);
      final l = await c.listSessions();
      return [for (final s in l.sessions) s.name]..sort();
    });

/// Peek at another mailbox (admin scope only; no state change).
typedef PeekKey = ({String hostId, String mailbox});
final peekProvider = FutureProvider.autoDispose.family<MessagePage, PeekKey>(
  (ref, key) async =>
      (await ref.watch(hostClientProvider(key.hostId).future))
          .listMailbox(all: true, mailbox: key.mailbox),
);

sealed class MailResult {
  const MailResult();
}

class MailOk extends MailResult {
  const MailOk(this.message);
  final Message message;
}

class MailFailed extends MailResult {
  const MailFailed(this.error, {this.outcomeUnknown = false});
  final String error;
  final bool outcomeUnknown;
}

/// `recipient` charset for humans: `human:<name>`, name `[a-zA-Z0-9._-]{1,64}`.
final humanRecipientRe = RegExp(r'^human:[a-zA-Z0-9._-]{1,64}$');
final sessionRecipientRe = RegExp(r'^[a-zA-Z0-9._@-]+$');

bool validRecipient(String to) =>
    humanRecipientRe.hasMatch(to) ||
    (!to.startsWith('human:') && sessionRecipientRe.hasMatch(to));

/// Mark read / reply / send. Writes carry an `Idempotency-Key` that stays the
/// same while the same payload is retried (lost answer, error), and is
/// dropped after a success or when the owning screen goes away.
class MailboxActions {
  MailboxActions(this._ref);
  final Ref _ref;
  final _keys = IdempotencyKeys();

  Future<AgentdClient> _client(String id) =>
      _ref.read(hostClientProvider(id).future);

  Future<MailResult> markRead(String hostId, int id) async {
    try {
      final m = await (await _client(hostId)).markMessageRead(id);
      _ref.read(mailboxFeedProvider.notifier).update(hostId, m);
      return MailOk(m);
    } on Object catch (e) {
      return MailFailed(describeMailboxError(e));
    }
  }

  Future<MailResult> reply(String hostId, int id, String body) async {
    final scope = 'reply:$hostId:$id';
    final key = _keys.keyFor(scope, body);
    try {
      final m = await (await _client(hostId))
          .replyMessage(id, body, idempotencyKey: key);
      _keys.clear(scope);
      // The original is now `replied`.
      unawaited(_refreshOriginal(hostId, id));
      return MailOk(m);
    } on Object catch (e) {
      return MailFailed(
        describeMailboxError(e),
        outcomeUnknown: e is ApiException && e.isOutcomeUnknown,
      );
    }
  }

  /// [fresh] drops the stored key first: an explicit "Send again (new
  /// message)" after a lost answer, which may deliver a second copy.
  Future<MailResult> send(
    String hostId,
    SendRequest req, {
    bool fresh = false,
  }) async {
    final scope = 'send:$hostId';
    if (fresh) _keys.clear(scope);
    final key = _keys.keyFor(scope, jsonEncode(req.toJson()));
    try {
      final m = await (await _client(hostId))
          .sendMessage(req, idempotencyKey: key);
      _keys.clear(scope);
      return MailOk(m);
    } on Object catch (e) {
      return MailFailed(
        describeMailboxError(e),
        outcomeUnknown: e is ApiException && e.isOutcomeUnknown,
      );
    }
  }

  Future<void> _refreshOriginal(String hostId, int id) async {
    try {
      final m = await (await _client(hostId)).getMessage(id);
      _ref.read(mailboxFeedProvider.notifier).update(hostId, m);
    } on Object {
      // Cosmetic only.
    }
  }
}

/// Auto-disposed: the idempotency keys live only as long as a screen watches
/// this provider (compose / message screens do, in `build`), so a key never
/// outlives the compose form it belongs to.
final mailboxActionsProvider = Provider.autoDispose<MailboxActions>(
  MailboxActions.new,
);
