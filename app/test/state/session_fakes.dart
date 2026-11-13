import 'dart:async';
import 'dart:convert';

import 'package:agentd_app/api/agentd_api.dart';
import 'package:agentd_app/state/session_view.dart';

class FakeSessionGateway implements SessionGateway {
  // ignore: close_sinks
  final events = StreamController<SseEvent>.broadcast();
  void Function(SseState)? onState;
  int opened = 0;
  int fetches = 0;
  int acks = 0;
  View fetched = makeView();
  final acts = <({ActRequest req, String key})>[];

  /// Next act results/errors, consumed in order; empty = success.
  final actQueue = <Object>[];

  @override
  Stream<SseEvent> viewEvents({void Function(SseState)? onState}) {
    opened++;
    this.onState = onState;
    return events.stream;
  }

  @override
  Future<View> fetchView() async {
    fetches++;
    return fetched;
  }

  @override
  Future<ActResult> act(
    ActRequest req, {
    required String idempotencyKey,
  }) async {
    acts.add((req: req, key: idempotencyKey));
    if (actQueue.isNotEmpty) {
      final n = actQueue.removeAt(0);
      if (n is Exception) throw n;
    }
    return const ActResult(action: 'x');
  }

  @override
  Future<void> ack() async => acks++;

  String terminalText = 'RAW SCREEN';
  @override
  Future<TerminalSnapshot> terminal() async =>
      TerminalSnapshot(text: terminalText);

  void sendView(View v) =>
      events.add(SseEvent(event: 'view', data: jsonEncode(v.toJson())));

  void sendPatch(Map<String, Object?> j) =>
      events.add(SseEvent(event: 'view.patch', data: jsonEncode(j)));
}

View makeView({
  int revision = 1,
  String epoch = 'e1',
  Status status = Status.idle,
  List<String> lines = const ['hello'],
  Input input = const Input(),
  List<Option> options = const [],
  Dialog? dialog,
  bool attention = false,
  int? exitCode,
}) => View(
  name: 's',
  epoch: epoch,
  revision: revision,
  status: status,
  statusRaw: status.wire,
  attention: Attention(set: attention),
  lines: [for (final l in lines) Line(text: l)],
  input: input,
  options: options,
  dialog: dialog,
  exitCode: exitCode,
);

const optA = Option(
  id: 'o1',
  label: 'Yes',
  index: 1,
  kind: OptionKind.numbered,
  selected: true,
  confidence: 0.9,
);
const optB = Option(
  id: 'o2',
  label: 'No',
  index: 2,
  kind: OptionKind.numbered,
  confidence: 0.9,
);
