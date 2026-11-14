require "test_helper"

# The Go port's tests write a corpus of uploads, sign-in documents, JSON
# documents and machine keys (REPORT_PARITY_DIR/corpus, from
# internal/reports/parity_test.go). This writes the site's verdict on each
# beside it (rails-verdicts.jsonl); CI compares that file with the port's
# expectations (internal/reports/testdata/rails-verdicts.jsonl). Skipped
# without the folder.
class ReportParityTest < ActionDispatch::IntegrationTest
  SECRET_KEY_BASE = "report parity: not a secret"

  test "the site's verdicts on the parity corpus" do
    dir = ENV["REPORT_PARITY_DIR"] or skip "REPORT_PARITY_DIR is not set"
    corpus = Pathname(dir).join("corpus")
    secret = Rails.application.config.secret_key_base
    Rails.application.config.secret_key_base = SECRET_KEY_BASE

    paths = Dir[corpus.join("*", "*")].sort
    assert_operator paths.size, :>, 100
    lines = paths.each_with_index.map do |path, n|
      name = Pathname(path).relative_path_from(corpus).to_s
      JSON.generate({ "case" => name }.merge(send("judge_#{name.split("/").first}", File.binread(path), n)))
    end
    File.write(Pathname(dir).join("rails-verdicts.jsonl"), lines.join("\n") + "\n")
  ensure
    Rails.application.config.secret_key_base = secret if secret
  end

  private

  # An upload, through the API. A body that is not JSON is answered by Rails
  # before the controller sees it, so only its status is compared.
  def judge_reports(text, n)
    Api::V1::ReportsController::RATE_LIMITS.clear
    body = upload_report(text, ip: "10.9.#{n / 250}.#{n % 250 + 1}")
    return { "status" => 400 } if response.status == 400
    return { "status" => response.status, "error" => body["error"], "details" => body["details"] } unless response.status == 201

    report = Report.find_by!(public_id: body.fetch("id"))
    { "status" => 201, "machine_id" => report.machine_id, "canonical_sha256" => OpenSSL::Digest::SHA256.hexdigest(MachineSignature.canonical(report.body)) }
  end

  # A signed document that is not a report: its signature alone.
  def judge_signins(text, _n)
    signature = MachineSignature.verify!(JSON.parse(text), namespace: MachineSignature::TESTER_NAMESPACE)
    { "valid" => true, "machine_id" => signature.machine_id }
  rescue MachineSignature::Invalid => invalid
    { "valid" => false, "reason" => invalid.message }
  end

  def judge_canonical(text, _n)
    { "canonical" => MachineSignature.canonical(JSON.parse(text)) }
  rescue JSON::ParserError
    { "not_json" => true }
  rescue MachineSignature::Invalid => invalid
    { "invalid" => invalid.message }
  end

  def judge_keys(text, _n)
    { "machine_id" => MachineSignature.machine_id_for(Base64.strict_decode64(text.split.fetch(1))) }
  end
end
