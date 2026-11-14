require "test_helper"
require_relative "../support/aggregation_dump"

# The Go port's tests write the seeded corpus (db/preview_seeds.rb's reports,
# at fixed times) to PAGE_PARITY_DIR/corpus.json. This stores it, then writes
# beside it what the site counts from it (rails-aggregation.json) and the
# pinned elements of each public page (rails-pages.json): every element
# inside <main> with an id or a data- attribute, as tag, id, classes and
# data- attributes. CI compares both with the port's. Skipped without the folder.
class PageParityTest < ActionDispatch::IntegrationTest
  PAGES = [ "/", "/matrix", "/matrix?group=build", "/matrix?group=release", "/matrix?group=model&confirmed=1", "/matrix?group=build&state=works&layer=aurora",
            "/models", "/models/j416c", "/models/j613", "/features", "/features?model=j416c", "/features/gpu", "/features/brightness" ].freeze

  test "what the site counts and pins on the seeded corpus" do
    dir = ENV["PAGE_PARITY_DIR"] or skip "PAGE_PARITY_DIR is not set"
    dir = Pathname(dir)
    corpus = JSON.parse(dir.join("corpus.json").read)
    assert_operator corpus.size, :>, 10
    corpus.each do |entry|
      Tester.find_or_create_by!(login: entry["tester_login"]) if entry["verified"]
      report = Report.create!(body: entry["body"], schema_version: entry["body"]["schema_version"], machine_id: entry["machine_id"],
                              tester_login: entry["tester_login"], created_at: Time.iso8601(entry["created_at"]))
      report.update_columns(public_id: entry["public_id"])
    end

    File.write(dir.join("rails-aggregation.json"), JSON.pretty_generate(AggregationDump.dump))
    pages = PAGES.to_h do |path|
      Current.reset
      get path
      assert_response :success, path
      [ path, css_select("main *").filter_map { |element| pinned(element) } ]
    end
    File.write(dir.join("rails-pages.json"), JSON.pretty_generate(pages))
  end

  private

  def pinned(element)
    data = element.attributes.select { |name, _| name.start_with?("data-") }.transform_values(&:value)
    return if element["id"].blank? && data.empty?

    { "tag" => element.name, "id" => element["id"].to_s, "class" => element["class"].to_s.split.sort, "data" => data.sort.to_h }
  end
end
