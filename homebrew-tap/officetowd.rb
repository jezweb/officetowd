class Officetowd < Formula
  desc "Local-to-R2 bisync daemon for Office Town wikis (Goanna-style)"
  homepage "https://github.com/jezweb/officetowd"
  version "0.0.1-alpha"

  if Hardware::CPU.arm? && OS.mac?
    url "https://github.com/jezweb/officetowd/releases/download/v#{version}/officetowd-darwin-arm64.tar.gz"
    sha256 "TBD"
  elsif Hardware::CPU.intel? && OS.mac?
    url "https://github.com/jezweb/officetowd/releases/download/v#{version}/officetowd-darwin-amd64.tar.gz"
    sha256 "TBD"
  elsif OS.linux? && Hardware::CPU.arm?
    url "https://github.com/jezweb/officetowd/releases/download/v#{version}/officetowd-linux-arm64.tar.gz"
    sha256 "TBD"
  else
    url "https://github.com/jezweb/officetowd/releases/download/v#{version}/officetowd-linux-amd64.tar.gz"
    sha256 "TBD"
  end

  def install
    bin.install "officetowd"
  end

  test do
    assert_match "officetowd", shell_output("#{bin}/officetowd version")
  end
end
