class Goose < Formula
  desc "Menubar app for GitHub pull request tracking and notifications"
  homepage "https://github.com/codeGROOVE-dev/goose"
  url "https://github.com/ready-to-review/goose.git",
      tag:      "v3.7.1",
      revision: "920467c86e2123db4d47503de341bfb5aca79b42"
  license "GPL-3.0-only"
  head "https://github.com/ready-to-review/goose.git", branch: "main"

  depends_on "go" => :build
  depends_on "gh"

  def install
    ldflags = %W[
      -X main.version=#{version}
      -X main.commit=#{Utils.git_short_head}
      -X main.date=#{time.iso8601}
    ]

    system "go", "build", *std_go_args(ldflags:, output: bin/"goose"), "./cmd/goose"
  end

  test do
    output = shell_output("#{bin}/goose --version")
    assert_match version.to_s, output
  end
end
