/// Live semantic view of one session plus the `act` flow
/// (frontend-app.md sections 2 and 4.3, http-api.md sections 3.5 to 3.6).
library;

import 'dart:async';

import 'package:flutter_riverpod/flutter_riverpod.dart';
import 'package:uuid/uuid.dart';

import '../api/agentd_api.dart';
import 'providers.dart';

/// Family argument of [sessionViewProvider]: a session on a paired host.
typedef SessionKey = ({String hostId, String name});

SessionKey sessionKey(String hostId, String name) =>
    (hostId: hostId, name: name);

/// Lines requested for the initial view and for refetches. The list widget is
/// lazy, so this only bounds the transfer; older history is out of v1.
const sessionViewTail = 1000;

/// Options below this confidence need an explicit confirm tap.
const lowConfidenceThreshold = 0.75;

/// Seam between the notifier and the HTTP client (replaced by a fake in tests).
abstract class SessionGateway {
  /// `view/stream` frames (`view`, `view.patch`, `resync`, `bye`...). Resumes
  /// by itself after a drop; [onState] reports the connection state.
  Stream<SseEvent> viewEvents({void Function(SseState)? onState});

  /// Full view (`GET .../view`).
  Future<View> fetchView();

  Future<ActResult> act(ActRequest req, {required String idempotencyKey});

  Future<void> ack();

  /// Plain text of the real terminal screen (`GET .../terminal`, read-only).
  Future<TerminalSnapshot> terminal();
}

class ClientSessionGateway implements SessionGateway {
  ClientSessionGateway(this.client, this.name);
  final AgentdClient client;
  final String name;

  @override
  Stream<SseEvent> viewEvents({void Function(SseState)? onState}) async* {
    final c = client.viewStream(name, tail: sessionViewTail);
    final sub = onState == null ? null : c.states.listen(onState);
    try {
      yield* c.events();
    } finally {
      await sub?.cancel();
    }
  }

  @override
  Future<View> fetchView() async {
    final r = await client.getView(name, lines: sessionViewTail);
    if (r is ViewFull) return r.view;
    throw StateError('unexpected view answer ${r.runtimeType}');
  }

  @override
  Future<ActResult> act(ActRequest req, {required String idempotencyKey}) =>
      client.act(name, req, idempotencyKey: idempotencyKey);

  @override
  Future<void> ack() async {
    await client.ackView(name);
  }

  @override
  Future<TerminalSnapshot> terminal() => client.getTerminal(name);
}

final sessionGatewayProvider = FutureProvider.autoDispose
    .family<SessionGateway, SessionKey>((ref, key) async {
      final client = await ref.watch(hostClientProvider(key.hostId).future);
      return ClientSessionGateway(client, key.name);
    });

enum FeedbackKind { info, warning, error, review }

/// Message about the last user action, shown above the composer.
class ActFeedback {
  const ActFeedback(this.kind, this.message);
  final FeedbackKind kind;
  final String message;
}

class SessionViewState {
  const SessionViewState({
    this.view,
    this.loading = true,
    this.error,
    this.reconnecting = false,
    this.acting = false,
    this.feedback,
    this.canRetry = false,
    this.reviewAgain = false,
    this.unknownText,
    this.sentSeq = 0,
    this.sentText,
  });

  /// Latest view; null until the first one arrives.
  final View? view;
  final bool loading;

  /// Terminal stream failure (not found, unauthorized...).
  final String? error;
  final bool reconnecting;

  /// An `act` is in flight.
  final bool acting;
  final ActFeedback? feedback;

  /// The last failed action may be repeated with the same idempotency key.
  final bool canRetry;

  /// The options changed under the user's finger: highlight "review again".
  final bool reviewAgain;

  /// Prompt text whose result is unknown (the answer was lost): shown as a
  /// "Sent? result unknown" note with an explicit "Send again". The composer
  /// never keeps this text primed.
  final String? unknownText;

  /// Counts accepted prompts; [sentText] is the last one. The composer clears
  /// itself when it still holds exactly that text.
  final int sentSeq;
  final String? sentText;

  SessionViewState copyWith({
    View? view,
    bool? loading,
    Object? error = _keep,
    bool? reconnecting,
    bool? acting,
    Object? feedback = _keep,
    bool? canRetry,
    bool? reviewAgain,
    Object? unknownText = _keep,
    int? sentSeq,
    String? sentText,
  }) => SessionViewState(
    view: view ?? this.view,
    loading: loading ?? this.loading,
    error: identical(error, _keep) ? this.error : error as String?,
    reconnecting: reconnecting ?? this.reconnecting,
    acting: acting ?? this.acting,
    feedback: identical(feedback, _keep)
        ? this.feedback
        : feedback as ActFeedback?,
    canRetry: canRetry ?? this.canRetry,
    reviewAgain: reviewAgain ?? this.reviewAgain,
    unknownText: identical(unknownText, _keep)
        ? this.unknownText
        : unknownText as String?,
    sentSeq: sentSeq ?? this.sentSeq,
    sentText: sentText ?? this.sentText,
  );

  static const _keep = Object();
}

final sessionViewProvider = NotifierProvider.autoDispose
    .family<SessionViewNotifier, SessionViewState, SessionKey>(
      SessionViewNotifier.new,
    );

/// A refused prompt that may be repeated with the same idempotency key.
class _Pending {
  _Pending(this.req, this.key, this.at);
  final ActRequest req;
  final String key;
  final DateTime at;
}

/// How long the same key may be reused for an explicit "Retry".
const retrySameKeyWindow = Duration(seconds: 30);

enum _Res { ok, unknown, failed }

/// Clock seam for [retrySameKeyWindow] (tests).
final sessionClockProvider = Provider<DateTime Function()>(
  (ref) => DateTime.now,
);

class SessionViewNotifier extends Notifier<SessionViewState> {
  SessionViewNotifier(this.key);
  final SessionKey key;

  static const _uuid = Uuid();
  static const ackDebounce = Duration(milliseconds: 500);

  /// The screen must have been visible this long before attention is acked.
  static const minVisibleBeforeAck = Duration(seconds: 1);

  final _vs = ViewState();
  SessionGateway? _gw;
  StreamSubscription<SseEvent>? _sub;
  bool _disposed = false;
  int _gen = 0;
  bool _refetching = false;
  bool _visible = true;
  bool _visibleLongEnough = false;
  Timer? _visibleTimer;
  Timer? _ackTimer;
  int _lastAckRev = -1;
  _Pending? _pending;

  @override
  SessionViewState build() {
    ref.onDispose(() {
      _disposed = true;
      _gen++;
      _ackTimer?.cancel();
      _visibleTimer?.cancel();
      _sub?.cancel();
    });
    scheduleMicrotask(_start);
    return const SessionViewState();
  }

  Future<void> _start() async {
    if (_disposed) return;
    final gen = ++_gen;
    await _sub?.cancel();
    try {
      _gw ??= await ref.read(sessionGatewayProvider(key).future);
    } on Object catch (e) {
      if (!_disposed && gen == _gen) _fail(e);
      return;
    }
    if (_disposed || gen != _gen) return;
    _sub = _gw!
        .viewEvents(
          onState: (s) {
            if (_disposed || gen != _gen) return;
            final rec = s == SseState.reconnecting || s == SseState.connecting;
            if (rec != state.reconnecting) {
              state = state.copyWith(reconnecting: rec && state.view != null);
            }
          },
        )
        .listen(
          (e) => _onEvent(gen, e),
          onError: (Object e) {
            if (!_disposed && gen == _gen) _fail(e);
          },
        );
  }

  void _fail(Object e) {
    final msg = e is ApiException
        ? (e.isNotFound
              ? 'This session no longer exists.'
              : e.isUnauthenticated
              ? 'The host rejected this device: re-pair it.'
              : e.message)
        : e is PinMismatchException
        ? 'The host certificate changed (pin mismatch).'
        : e is ConnectionException
        ? e.message
        : '$e';
    state = state.copyWith(loading: false, error: msg, reconnecting: false);
  }

  void _onEvent(int gen, SseEvent e) {
    if (_disposed || gen != _gen) return;
    if (e.event == 'resync' || e.event == 'bye') {
      unawaited(_refetch());
      return;
    }
    final out = _vs.handle(e);
    if (out == PatchOutcome.needsRefetch) {
      unawaited(_refetch());
    } else if (out == PatchOutcome.applied) {
      _publish();
    }
  }

  Future<void> _refetch() async {
    final gw = _gw;
    if (gw == null || _refetching || _disposed) return;
    _refetching = true;
    try {
      final v = await gw.fetchView();
      if (_disposed) return;
      _vs.reset(v);
      _publish();
    } on Object catch (e) {
      if (!_disposed && e is ApiException && e.isNotFound) _fail(e);
      // Other failures: the stream keeps running and will resync.
    } finally {
      _refetching = false;
    }
  }

  void _publish() {
    final v = _vs.view;
    if (v == null) return;
    state = state.copyWith(
      view: v,
      loading: false,
      error: null,
      reconnecting: false,
    );
    _maybeAck();
  }

  // ------------------------------------------------------------ lifecycle

  /// Drops the stream and opens a new one (app resumed, network changed).
  Future<void> reconnect() async {
    if (_disposed) return;
    await _start();
    unawaited(_refetch());
  }

  /// The screen is on top and the app is in the foreground: attention may be
  /// acknowledged (view/ack, debounced).
  void setVisible(bool visible) {
    if (visible && _visible && (_visibleTimer != null || _visibleLongEnough)) {
      return; // already on top and counting
    }
    _visible = visible;
    _visibleTimer?.cancel();
    _visibleTimer = null;
    _visibleLongEnough = false;
    if (visible) {
      // Attention is acknowledged only once the screen has really been on
      // top for a moment (a deep link must not clear it by merely opening).
      _visibleTimer = Timer(minVisibleBeforeAck, () {
        _visibleLongEnough = true;
        _maybeAck();
      });
    }
  }

  void _maybeAck() {
    final v = state.view;
    if (!_visible ||
        !_visibleLongEnough ||
        v == null ||
        !v.attention.set ||
        v.revision == _lastAckRev ||
        _ackTimer != null) {
      return;
    }
    _ackTimer = Timer(ackDebounce, () async {
      _ackTimer = null;
      final cur = state.view;
      if (_disposed ||
          !_visible ||
          !_visibleLongEnough ||
          cur == null ||
          !cur.attention.set) {
        return;
      }
      _lastAckRev = cur.revision;
      try {
        await _gw?.ack();
      } on Object {
        // Best effort: the next view retries.
        _lastAckRev = -1;
      }
    });
  }

  // ------------------------------------------------------------------ act
  //
  // Every action carries the `revision` + `epoch` of the view DISPLAYED when
  // the user tapped (the daemon refuses with 409/412 when the interaction
  // state moved on). Nothing is ever sent twice on its own: no auto retry.

  void dismissFeedback() {
    state = state.copyWith(feedback: null, canRetry: false, reviewAgain: false);
    _pending = null;
  }

  void dismissUnknown() => state = state.copyWith(unknownText: null);

  ({int revision, String epoch})? _at(View? shown) {
    final v = shown ?? state.view;
    return v == null ? null : (revision: v.revision, epoch: v.epoch);
  }

  /// True when the view changed between the tap and now (a confirm dialog
  /// was open): nothing is sent, the user must look again.
  bool _movedOn(View? shown) {
    final cur = state.view;
    if (shown == null || cur == null) return false;
    if (shown.revision == cur.revision && shown.epoch == cur.epoch) {
      return false;
    }
    state = state.copyWith(
      feedback: const ActFeedback(
        FeedbackKind.review,
        'The screen changed while you were confirming: review it again. '
        'Nothing was sent.',
      ),
      canRetry: false,
      reviewAgain: true,
    );
    _pending = null;
    return true;
  }

  /// Types [text] into the session and submits it. True when the text left
  /// the composer: accepted, or sent with an unknown result (then it lives in
  /// [SessionViewState.unknownText]).
  Future<bool> prompt(String text) async {
    final at = _at(null);
    if (at == null) return false;
    final r = await _perform(
      ActRequest.prompt(text, revision: at.revision, epoch: at.epoch),
      _uuid.v4(),
    );
    return r != _Res.failed;
  }

  /// Sends the "result unknown" text again as a NEW request (new key).
  Future<bool> sendAgain() async {
    final text = state.unknownText;
    final at = _at(null);
    if (text == null || at == null) return false;
    final r = await _perform(
      ActRequest.prompt(text, revision: at.revision, epoch: at.epoch),
      _uuid.v4(),
    );
    if (r == _Res.ok) state = state.copyWith(unknownText: null);
    return r == _Res.ok;
  }

  /// Picks [o] on the view the user was looking at ([shown], captured at tap
  /// time before any confirm dialog; defaults to the current view).
  Future<bool> choose(Option o, {View? shown}) async {
    if (_movedOn(shown)) return false;
    final at = _at(shown);
    if (at == null) return false;
    return await _perform(
          ActRequest.choose(
            o.id,
            label: o.label,
            revision: at.revision,
            epoch: at.epoch,
          ),
          _uuid.v4(),
        ) ==
        _Res.ok;
  }

  Future<bool> keys(List<String> keys, {View? shown}) async {
    if (_movedOn(shown)) return false;
    final at = _at(shown);
    if (at == null) return false;
    return await _perform(
          ActRequest.keys(keys, revision: at.revision, epoch: at.epoch),
          _uuid.v4(),
        ) ==
        _Res.ok;
  }

  Future<bool> interrupt({View? shown}) async {
    if (_movedOn(shown)) return false;
    final at = _at(shown);
    if (at == null) return false;
    return await _perform(
          ActRequest.interrupt(revision: at.revision, epoch: at.epoch),
          _uuid.v4(),
        ) ==
        _Res.ok;
  }

  Future<bool> cancelMenu({View? shown}) async {
    if (_movedOn(shown)) return false;
    final at = _at(shown);
    if (at == null) return false;
    return await _perform(
          ActRequest.cancel(revision: at.revision, epoch: at.epoch),
          _uuid.v4(),
        ) ==
        _Res.ok;
  }

  /// Explicit "Retry" of a refused PROMPT with the same untouched text and
  /// the same key, only within [retrySameKeyWindow]. Other actions are never
  /// retried: the user taps again on a fresh view.
  Future<bool> retry() async {
    final p = _pending;
    if (p == null) return false;
    if (ref.read(sessionClockProvider)().difference(p.at) >
        retrySameKeyWindow) {
      _pending = null;
      state = state.copyWith(
        feedback: const ActFeedback(
          FeedbackKind.info,
          'That was a while ago: check the screen and tap Send again.',
        ),
        canRetry: false,
      );
      return false;
    }
    return await _perform(p.req, p.key) != _Res.failed;
  }

  Future<_Res> _perform(ActRequest req, String idemKey) async {
    final gw = _gw;
    if (gw == null || state.acting) return _Res.failed;
    _pending = null;
    state = state.copyWith(
      acting: true,
      feedback: null,
      canRetry: false,
      reviewAgain: false,
    );
    try {
      final r = await gw.act(req, idempotencyKey: idemKey);
      if (_disposed) return _Res.ok;
      state = state.copyWith(
        acting: false,
        feedback: r.warning == null || r.warning!.isEmpty
            ? null
            : ActFeedback(FeedbackKind.warning, r.warning!),
        sentSeq: req.action == ActAction.prompt ? state.sentSeq + 1 : null,
        sentText: req.action == ActAction.prompt ? req.text : null,
      );
      unawaited(_refetch());
      return _Res.ok;
    } on Object catch (e) {
      if (_disposed) return _Res.failed;
      return _handleActError(e, req, idemKey);
    }
  }

  _Res _handleActError(Object e, ActRequest req, String idemKey) {
    // Only a prompt may be repeated (same key, same text, explicit tap).
    final isPrompt = req.action == ActAction.prompt;
    var feedback = ActFeedback(FeedbackKind.error, 'Action failed: $e');
    var retry = false;
    var review = false;
    var res = _Res.failed;
    String? unknown;
    if (e is ApiException) {
      final code = e.code;
      if (e.isOutcomeUnknown || code == 'timeout' || e.statusCode == 504) {
        // Never retried: the input may already have reached the terminal.
        feedback = const ActFeedback(
          FeedbackKind.warning,
          'Result unknown: check the screen before trying again.',
        );
        if (isPrompt) {
          unknown = req.text;
          res = _Res.unknown;
        }
        unawaited(_refetch());
        final fresh = e.freshView;
        if (fresh != null) _vs.reset(fresh);
      } else if (e.isConflict || e.isPreconditionFailed) {
        final fresh = e.freshView;
        switch (code) {
          case 'busy' || 'human_draft':
            feedback = const ActFeedback(
              FeedbackKind.warning,
              'Someone is typing on the host — try again when they finish.',
            );
            retry = isPrompt;
          case 'not_ready':
            feedback = const ActFeedback(
              FeedbackKind.warning,
              'The agent is not ready for that right now.',
            );
          case 'exited':
            feedback = const ActFeedback(
              FeedbackKind.info,
              'The session has exited.',
            );
            unawaited(_refetch());
          case 'starting':
            feedback = const ActFeedback(
              FeedbackKind.info,
              'The session is still starting. Try again in a moment.',
            );
            retry = isPrompt;
          case 'in_progress':
            feedback = const ActFeedback(
              FeedbackKind.info,
              'Still processing the previous attempt: wait a moment and '
              'check the screen.',
            );
            retry = isPrompt;
          case 'stale_revision' || 'option_mismatch' || 'conflict':
            review = true;
          default:
            if (e.isPreconditionFailed) {
              review = true;
            } else {
              // Unknown 409: nothing is assumed about it.
              feedback = ActFeedback(
                FeedbackKind.error,
                'The host refused this action (${e.code}).',
              );
            }
        }
        if (review) {
          // The world moved: replace the view, never resubmit.
          feedback = const ActFeedback(
            FeedbackKind.review,
            'The screen changed: review it and act again.',
          );
          if (fresh != null) {
            _vs.reset(fresh);
          } else {
            unawaited(_refetch());
          }
        }
      } else if (code == 'busy') {
        feedback = const ActFeedback(
          FeedbackKind.warning,
          'Someone is typing on the host — try again when they finish.',
        );
        retry = isPrompt;
      } else if (code == 'not_ready') {
        feedback = const ActFeedback(
          FeedbackKind.warning,
          'The agent is not ready for that right now.',
        );
      } else if (code == 'verify_failed' ||
          code == 'paste_unverified' ||
          e.statusCode == 422 && code != 'idempotency_mismatch') {
        feedback = const ActFeedback(
          FeedbackKind.error,
          'The text never showed up in the terminal and nothing was '
          'submitted.',
        );
        retry = isPrompt;
      } else if (e.isForbidden) {
        feedback = const ActFeedback(
          FeedbackKind.error,
          'This device has read-only access.',
        );
      } else if (e.isNotFound) {
        feedback = const ActFeedback(
          FeedbackKind.error,
          'This session no longer exists.',
        );
      } else {
        feedback = ActFeedback(FeedbackKind.error, e.message);
        retry = isPrompt && e.retryable;
      }
    } else if (e is ConnectionException) {
      feedback = ActFeedback(
        FeedbackKind.error,
        'Cannot reach the host: ${e.message}. Nothing was sent.',
      );
      retry = isPrompt;
    }
    if (retry) {
      _pending = _Pending(req, idemKey, ref.read(sessionClockProvider)());
    }
    final next = state.copyWith(
      acting: false,
      feedback: feedback,
      canRetry: retry,
      reviewAgain: review,
    );
    state = unknown == null ? next : next.copyWith(unknownText: unknown);
    if (_vs.view != null && !identical(_vs.view, state.view)) _publish();
    return res;
  }
}
