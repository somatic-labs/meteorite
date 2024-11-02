# meteorite

![image](https://github.com/user-attachments/assets/baa314c4-4e78-4ca5-ba8c-96380ec49bd1)


## Overview

meteorite is a powerful tool designed to empower users to test and validate the performance and security claims of Cosmos-based blockchains. By providing an easy-to-use testing suite, meteorite enables advanced users to ensure that the blockchains they depend on are robust, secure, and capable of handling real-world conditions.

## Features

- **Decentralized File Storage Testing**: Utilize mainnet state to test decentralized file storage capabilities.
- **Performance Testing**: Simplify configuration for comprehensive performance evaluations.
- **Fee Market Module Testing**: Verify that gas prices increase as expected to prevent peer-to-peer (P2P) storms, ensuring network stability.
- **IBC Security Testing**: Assess the safety of a chain against potential threats like the "Banana King" exploit.
- **CometBFT Testing**: Evaluate chain resilience against P2P storms and other network stressors.

## Installation

To install meteorite, run the following commands:

```bash
git clone https://github.com/somatic-labs/meteorite
cd meteorite
go install ./...
```



## Usage

meteorite comes with pre-configured mainnet settings available in the `configurations` folder. To get started:

1. Ensure you have a file named `seedphrase` containing your seed phrase.
2. *(Optional)* Set up your own node with a larger mempool (e.g., 10 GB) that accepts a higher number of transactions (e.g., 50,000).
3. Edit the `nodes.toml` file to include your RPC URLs and adjust any other necessary settings.
4. Run `meteorite` in the same directory as your `nodes.toml` and `seedphrase` files.

This will initiate the testing suite with your specified configurations.

## Important Notes

- **Responsible Use**: meteorite is designed for use on mainnets.  The tokens you own grant you the right to make any valid transaction.  Like any user of any chain, meteorite can only make valid transactions.  
  * Test your favorite mainnet, yourself, and make sure that you can't get Luna'd
  * Test your favorite mainnet, yourself, and make sure that you can't get Levana'd

Over $70,001,400,000 has been lost to the class of issues named p2p storms.  

If you're investing, test the chain.  It's free if you tweet about using meteorite.

- **Valid Transactions Only**: The tool operates within the bounds of valid transactions explicitly supported by the chains it tests.
- **Reporting Issues**: For questions about meteorite's capabilities or to report potential security issues, please contact the project maintainers through the appropriate channels listed in this repository.

## Background

meteorite was developed to enhance the testing capabilities for Cosmos-based blockchains after identifying areas where additional testing tools were needed. By simulating various scenarios, meteorite helps developers and users alike to better understand the limits and robustness of their chains.

### Specific Tests Include:

- **Banana King Exploit Testing**: Initially reported in 2022, this test ensures that chains are secure against known exploits.
- **P2P Storms Testing**: Reported in 2021 and observed in networks like Luna Classic (2022) and Stride (2023), this test evaluates the chain's ability to handle network stress.

## Outcomes

The release of meteorite has contributed to:

- **Improved Awareness**: Highlighting potential vulnerabilities and encouraging proactive improvements in network security.
- **Enhanced Security Measures**: Prompting fixes for issues like P2P storms after thorough testing and community engagement.

Additional information is available at [faddat/fasf-report](https://github.com/faddat/fasf-report).

## Contributions and Feedback

We welcome contributions from the community to enhance meteorite's features and capabilities. If you'd like to contribute or have feedback, please open an issue or submit a pull request on GitHub.

---
