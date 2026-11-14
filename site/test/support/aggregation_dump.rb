# What the public pages count, as plain data: the builds, the matrix in each
# grouping and under each filter, the bring-up table, the filter bar's
# choices and the open regressions. The Go port writes the same from the same
# reports (internal/reports/aggregation_parity_test.go) and the two are
# compared. Used by page_parity_test.rb, and without Rails by the harness
# that loads the models alone.
module AggregationDump
  VIEWS = [
    [ "model", {} ], [ "release", {} ], [ "build", {} ],
    [ "build", { "confirmed" => "1" } ], [ "model", { "confirmed" => "1" } ], [ "release", { "confirmed" => "1" } ],
    [ "build", { "state" => "works" } ], [ "build", { "state" => "gap" } ], [ "build", { "state" => "untested" } ], [ "build", { "state" => "fails" } ],
    [ "model", { "state" => "regression" } ], [ "model", { "state" => "partial" } ],
    [ "build", { "layer" => "aurora" } ], [ "model", { "layer" => "omarchy", "state" => "works" } ],
    [ "build", { "runs" => "tester" } ], [ "model", { "runs" => "tester" } ], [ "model", { "runs" => "community" } ],
    [ "build", { "gen" => "m1" } ], [ "model", { "gen" => "M2" } ], [ "model", { "kernel" => "7.1.12-2-11-ARCH" } ],
    [ "build", { "from" => "2026-09-27", "to" => "2026-09-27" } ], [ "release", { "tool" => "0.1.8" } ], [ "build", { "chip" => "M1 Pro", "soc" => "t6000" } ],
    [ "build", { "model" => "j416c" } ], [ "model", { "model" => "j416c", "confirmed" => "1" } ],
    [ "build", { "build" => "1937418.362376005140001" } ], [ "build", { "set" => "apple-test-0052582276d9-20260927" } ],
    [ "build", { "release" => "4.0.0-alpha", "stack" => "converged" } ], [ "build", { "stack" => "mx-mac" } ], [ "model", { "model" => "j999" } ]
  ].freeze

  def self.dump
    reports = Report.visible.to_a
    {
      "runs" => reports.sort_by(&:upload_order).map { |report| run(report) },
      "matrices" => VIEWS.to_h { |group, params| [ name(group, params), matrix(reports, group, params) ] },
      "history" => VIEWS.select { |group, _| group == "model" }.to_h { |group, params| [ name(group, params), history(reports, params) ] },
      "lineup" => MacLineup.new(reports).rows.map { |row| lineup(row) },
      "options" => { "all" => options(FilterOptions.new(reports), ReportFilter::KEYS - %w[from to confirmed]),
                     "features model=j416c" => options(FilterOptions.new(reports, filter: ReportFilter.new({ "model" => "j416c" }, keys: ReportFilter::FEATURE_KEYS)), ReportFilter::FEATURE_KEYS) },
      "regressions" => Regressions.open(reports).map do |open|
        { "feature" => open.feature_id, "board" => open.board, "stack" => open.stack, "runs" => open.reports.map(&:public_id), "pass" => open.pass&.public_id }
      end
    }
  end

  def self.name(group, params) = ([ group ] + params.map { |key, value| "#{key}=#{value}" }).join(" ")

  def self.run(report)
    build = report.build
    counts = ->(confirmed) { report.result_counts(confirmed:).then { |c| %w[pass fail gap skip].to_h { |key| [ key, c[key] ] } } }
    { "id" => report.public_id, "release" => report.omarchy_version, "kernel" => report.kernel, "tester" => report.tester?,
      "build" => build && { "id" => build.id, "label" => build.label, "words" => build.words, "set" => build.set, "set_source" => build.set_source,
                            "commit" => build.commit, "stamp" => build.stamp, "kernel" => build.kernel, "boot" => build.boot, "image" => build.image_words },
      "labels" => [ report.configuration.label, report.configuration(by_build: false).label, report.configuration(by_model: true).label ],
      "counts" => counts.(false), "confirmed_counts" => counts.(true),
      "states" => report.feature_states.reject { |feature, _| feature.nil? }.sort.to_h }
  end

  def self.matrix(reports, group, params)
    filter = ReportFilter.new(params)
    matrix = CompatibilityMatrix.visible(by_build: group == "build", by_model: group == "model", filter:, reports:)
    { "features" => matrix.features.map { |feature| feature["id"] }, "runs" => matrix.runs_count, "empty" => matrix.empty?,
      "chips" => filter.chips.map { |key, value, _| [ key, value ] }, "rows" => rows(matrix.rows, matrix.features, summary: group == "model") }
  end

  # The builds behind each model summary: the run filters only (MatrixController#show).
  def self.history(reports, params)
    filter = ReportFilter.new(ReportFilter.new(params).values, keys: ReportFilter::RUN_KEYS)
    matrix = CompatibilityMatrix.visible(filter:, reports:)
    rows(matrix.rows, matrix.features, summary: false)
  end

  def self.rows(rows, features, summary:)
    rows.map do |row|
      c = row.configuration
      { "board" => c.board, "stack" => c.stack, "version" => c.version, "build" => c.build, "build_id" => c.build_id, "label" => c.label,
        "model" => row.model_name, "chip" => row.chip, "soc" => row.soc, "runs" => row.reports.map(&:public_id),
        "cells" => features.to_h { |feature| [ feature["id"], cell(row.cell(feature["id"]), summary:) ] } }
    end
  end

  def self.cell(cell, summary: false)
    sources = cell.sources
    { "state" => cell.state, "tentative" => cell.tentative, "display" => cell.display_state, "class" => cell.css_class, "summary" => cell.summary,
      "counts" => cell.counts, "machines" => cell.machines, "tester_machines" => cell.tester_machines, "hidden" => cell.hidden?,
      "tallies" => cell.tallies.to_a, "tester_tallies" => cell.tester_tallies.to_a,
      "runs" => cell.reports.map(&:public_id), "sources" => sources.map(&:public_id), "kernels" => (sources.map(&:kernel).uniq if summary) }
  end

  def self.lineup(row)
    { "name" => row.name, "chip" => row.chip, "tested" => row.tested?,
      "boards" => row.boards.map do |board|
        { "board" => board.board, "runs" => board.reports.map(&:public_id), "tester_runs" => board.tester_runs,
          "results" => MacLineup::AREAS.keys.to_h { |area| [ area, cell(board.result(area)) ] } }
      end,
      "statuses" => MacLineup::AREAS.keys.to_h do |area|
        status = row.status(area)
        [ area, { "state" => status.state, "source" => status.source, "note" => status.note, "words" => status.words, "unconfirmed" => status.unconfirmed? ? true : false,
                  "summary" => status.cell&.summary } ]
      end }
  end

  def self.options(options, keys) = keys.to_h { |key| [ key, options.public_send(key).map { |value, words| [ value, words ] } ] }
end
