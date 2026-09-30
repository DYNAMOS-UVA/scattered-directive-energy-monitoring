import pandas as pd
import matplotlib.pyplot as plt
import configuration as conf
import json
import argparse
import utils
from scipy.stats import kendalltau
import numpy as np
import seaborn as sns
from matplotlib.patches import Patch
import matplotlib.patheffects as pe
from pathlib import Path

# PREFIX = "scattered_"
PREFIX = ""

CORRELATION_CONFIG = {
        "Baseline": f"{PREFIX}baseline_experiment",
        # "Event-driven": "baseline_event_driven",
        "FedBCD": f"{PREFIX}fed_bcd_experiment",
        "Overlap-FedBCD": f"{PREFIX}overlap_fed_bcd_experiment",
        "FedEncrypt": f"{PREFIX}es_fed_encrypt_experiment"
    }

# CORRELATION_CONFIG = {
#         "Baseline": "distributed_baseline_experiment",
#         # "Event-driven": "baseline_event_driven",
#         "FedBCD": "distributed_fed_bcd_experiment",
#         "Overlap-FedBCD": "distributed_overlap_fed_bcd_experiment",
#         "FedEncrypt": "distributed_fed_encrypt_experiment"
#     }

CORRELATION_COLUMNS = [
    ("total_carbon_emission_difference", 'Mean Carbon Emission (gCO2e/KWh)', "cab", 1000),
    ("execution_time", 'Mean Execution Time (s)', "ex", 1),
    ("accuracy", 'Mean Accuracy (%)', "acc", 1)
    ]

def main():
    plot_sidecar, plot_accuracies, plot_correlation, plot_box, plot_pareto, is_local, both_environments, plot_bar = _resolve_args()

    print(f"============= Generate plots =============")
    
    if plot_sidecar:
        _generate_sidecar_plot()

    if plot_correlation:
        # _generate_correlation_plot(is_local, both_environments)
        _generate_mean_correlation_plot(is_local, both_environments)
        # _generate_correlation_matrix(is_local, both_environments)
        # _generate_correlation_shrinked_matrix()

    if plot_accuracies:
        _generate_accuracies_plot()

    if plot_box:
        _generate_box_plot(is_local, both_environments)

    if plot_pareto:
        _generate_pareto_plot(is_local, both_environments)
        _generate_mean_pareto_plot(is_local, both_environments)

    if plot_bar:
        _generate_distributed_bar_plot(is_local, both_environments)
        # _generate_bar_plot()

def _generate_sidecar_plot():
    print("> Generating sidecar plot")
    data = pd.read_csv("output/data_metrics.csv")

    time_seconds = data.index * 30  

    plt.figure(figsize=(10, 6))

    min_time = time_seconds.min()  # This will be 0
    max_time = time_seconds.max()
    cutoff_time = 120  # The 120-second mark

    for column_name in data.columns:
        if column_name.endswith("_energy"):        
            energy_data = data[column_name]
            container_name = column_name.replace("_energy", "")
            
            plt.plot(time_seconds, energy_data, linestyle='-', label=container_name)


    plt.axvline(x=cutoff_time, color='red', linestyle='--', linewidth=2)
    plt.axvspan(min_time, cutoff_time, color='gray', alpha=0.15, label='Idle Period')
    plt.axvspan(cutoff_time, max_time, color='lightgray', alpha=0.15, label='Active Period')

    plt.xlabel('Time (Seconds)') 
    plt.ylabel('Energy (in J)')
    plt.grid(True, linestyle='--', alpha=0.7)
    plt.legend()

    # Display the plot
    file_name = f"{conf.PLOT_OUTPUT_FOLDER}/sidecar_plot.png"
    plt.savefig(file_name)
    plt.close()
    print(f"Plot saved in {file_name}!")

def _resolve_mode_name(is_local, both_environments = False):
    return "both" if both_environments else ("local" if is_local else "fabric")

def _generate_mean_correlation_plot(is_local, both_environments):
    for column_name, column_label, column_prefix, column_scale in CORRELATION_COLUMNS:
        _generate_mean_correlation_plot_by_column(column_name, column_label, column_prefix, column_scale, is_local, both_environments)  

def _generate_mean_correlation_plot_by_column(column_name, column_label, column_prefix, column_scale, is_local, both_environments):
    print(f"> Generating mean correlation plot for column : {column_name}")

    plt.figure(figsize=(6, 5))
    
    if not CORRELATION_CONFIG:
        print("Warning: No JSON files found in the output folder.")
        return

    x = []
    y = []

    def _plot(is_local: bool, include_mode_in_label: bool):
        for name, file_prefix in CORRELATION_CONFIG.items():
            total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local)
            
            x_val = total_metrics_data[column_name].mean()*column_scale
            y_val = total_metrics_data["total_energy_difference"].mean()
            
            x.append(x_val)
            y.append(y_val)

            if include_mode_in_label:
                name = f"{name}_{_resolve_mode_name(is_local)}"

            plt.scatter(x_val, y_val, label=name, alpha=0.7, s=100)

    _plot(is_local, both_environments)

    if both_environments:
        _plot(not is_local, both_environments)

    if len(x) > 1:        
        slope, intercept = np.polyfit(x, y, 1)
        
        x_line = np.linspace(min(x) * 0.9, max(x) * 1.1, 100)
        y_line = slope * x_line + intercept
        
        plt.plot(x_line, y_line, color='gray', linestyle='--', alpha=0.5, label=f"Linear Fit")

    plt.xlabel(f"Mean {column_label}")
    plt.ylabel("Mean Energy Consumption (J)") 
    
    plt.legend()
    plt.grid(True, linestyle='--', alpha=0.6)
    plt.tight_layout()

    file_name = f"paper_en_{column_prefix}_mean_correlation_{_resolve_mode_name(is_local, both_environments)}_plot"
    _save_plot(plt, file_name)
    plt.close()

def _generate_correlation_plot(is_local, both_environments):
    for column_name, column_label, column_prefix, column_scale in CORRELATION_COLUMNS:
        _generate_correlation_plot_by_column(column_name, column_label, column_prefix, column_scale, is_local, both_environments)   

def _generate_correlation_plot_by_column(column_name, column_label, column_prefix, column_scale, is_local, both_environments):
    print(f"> Generating correlation plot for column: {column_name}")
    plt.figure(figsize=(6, 5))
    
    x = []
    y = []

    def _plot(is_local: bool, include_mode_in_label: bool):
        for name, file_prefix in CORRELATION_CONFIG.items():
            total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local)
            
            x_val = total_metrics_data[column_name]*column_scale
            y_val = total_metrics_data["total_energy_difference"]
            
            x.extend(x_val.tolist())
            y.extend(y_val.tolist())

            if include_mode_in_label:
                name = f"{name}_{_resolve_mode_name(is_local)}"

            plt.scatter(x_val, y_val, label=name, alpha=0.7, s=30)

    _plot(is_local, both_environments)

    if both_environments:
        _plot(not is_local, both_environments)

    x = np.array(x)
    y = np.array(y)

    if len(x) > 1:
        tau, p_value = kendalltau(x, y)
        
        slope, intercept = np.polyfit(x, y, 1)
        
        x_line = np.linspace(x.min(), x.max(), 100)
        y_line = slope * x_line + intercept
        
        plt.plot(x_line, y_line, color='gray', linestyle='--', alpha=0.5, label=f"Linear Fit (τ={tau:.2f})")

    plt.xlabel(column_label)
    plt.ylabel("Energy Consumption (J)") 
    
    plt.legend()
    plt.grid(True, linestyle='--', alpha=0.6)
    plt.tight_layout()

    label = _resolve_mode_name(is_local, both_environments)

    file_name = f"paper_en_{column_prefix}_correlation_{label}_plot"
    _save_plot(plt, file_name)
    plt.close()

def _generate_correlation_matrix(is_local, both_environments):
    print("> Generating correlation matrix heatmap")
    
    combined_frames = []

    def _read_data(is_local):
        for name, file_prefix in CORRELATION_CONFIG.items():
            total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local)
            
            df_copy = total_metrics_data.copy()
            df_copy['experiment_type'] = name 
            combined_frames.append(df_copy)

    _read_data(is_local)

    if both_environments:
        _read_data(not is_local)

    if not combined_frames:
        return

    master_df = pd.concat(combined_frames, ignore_index=True)

    columns_to_include = [
        "total_carbon_emission_difference",
        "total_energy_difference",
        "execution_time"
    ]

    rename_columns = {
        "total_carbon_emission_difference": "Carbon Emission (gCO2e/KWh)",
        "total_energy_difference": "Energy Consumption (J)",
        "execution_time": "Execution Time (s)"
    }

    valid_columns = [col for col in columns_to_include if col in master_df.columns]

    filtered_df = master_df[valid_columns].rename(columns=rename_columns)

    corr_matrix = filtered_df.corr(method='kendall')
    corr_matrix = corr_matrix.iloc[::-1]

    plt.figure(figsize=(10, 10))

    # cmap_custom = sns.light_palette("seagreen", as_cmap=True)
    cmap_custom = sns.color_palette("vlag", as_cmap=True)
    
    sns.heatmap(
        corr_matrix, 
        annot=True,
        fmt=".3f",
        cmap=cmap_custom,
        vmin=-1, vmax=1,
        center=0,
        square=True,
        linewidths=0.5,      
        cbar_kws={"shrink": .8}
    )

    plt.tight_layout()

    file_name = f"correlation_{_resolve_mode_name(is_local, both_environments)}_matrix"
    _save_plot(plt, file_name)
    plt.close()

def _generate_correlation_shrinked_matrix():
    print("> Generating compact correlation matrix heatmap (Local vs Fabric)")
    
    # Define the specific metric pairs we want to correlate
    pairs = {
        "Energy Consumption \n Execution Time": ("total_energy_difference", "execution_time"),
        "Energy Consumption \n Carbon Emission": ("total_energy_difference", "total_carbon_emission_difference"),
        "Execution Time \n Carbon Emission": ("execution_time", "total_carbon_emission_difference")
    }
    
    results = {"Local": [], "Fabric": []}
    
    # Helper to calculate the 3 correlations for a specific environment
    def _get_env_correlations(is_local_flag, config=CORRELATION_CONFIG):
        frames = []
        for name, file_prefix in config.items():
            total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local_flag)
            if not total_metrics_data.empty:
                frames.append(total_metrics_data)
        
        if not frames:
            return [np.nan, np.nan, np.nan]
            
        env_df = pd.concat(frames, ignore_index=True)
        
        corrs = []
        for label, (col1, col2) in pairs.items():
            if col1 in env_df.columns and col2 in env_df.columns:
                corr_val = env_df[col1].corr(env_df[col2], method='kendall')
                corrs.append(corr_val)
            else:
                corrs.append(np.nan)
        return corrs

    # Calculate for both environments regardless of the script's flags 
    # (since the goal is explicitly to show Local vs Fabric side-by-side)
    results["Local"] = _get_env_correlations(True)
    results["Fabric"] = _get_env_correlations(False, {
        "Baseline": "scattered_baseline_experiment",
        # "Event-driven": "baseline_event_driven",
        "FedBCD": "scattered_fed_bcd_experiment",
        "Overlap-FedBCD": "scattered_overlap_fed_bcd_experiment",
        "FedEncrypt": "scattered_es_fed_encrypt_experiment"
    })
    
    corr_df = pd.DataFrame(results, index=pairs.keys()).T
    
    plt.figure(figsize=(5, 1.8))
    
    cmap_custom = sns.color_palette("vlag", as_cmap=True)
    
    sns.heatmap(
        corr_df, 
        annot=True,
        fmt=".3f",
        cmap=cmap_custom,
        vmin=-1, vmax=1,
        center=0,
        linewidths=0.5,      
        cbar_kws={"shrink": 1.0}
    )
    
    plt.yticks(rotation=0, fontsize=9)
    plt.xticks(rotation=12, fontsize=9)
    plt.tight_layout()

    _save_plot(plt, "correlation_matrix")
    plt.close()

def _generate_accuracies_plot():
    print("> Generating accuracies plot")

    _set_font_size(plt)
    plt.figure(figsize=(10, 6))
    
    json_files = {
        "Baseline": "experiments/fabric/scattered_baseline_experiment_260916_1427/experiments.json",
        "FedBCD": "experiments/fabric/scattered_fed_bcd_experiment_260919_0712/experiments.json",
        "Overlap-FedBCD": "experiments/fabric/scattered_overlap_fed_bcd_experiment_260919_2345/experiments.json",
        "FedEncrypt": "experiments/fabric/scattered_es_fed_encrypt_experiment_260920_1645/experiments.json"
    }
    
    if not json_files:
        print("Warning: No JSON files found in the output folder.")
        return


    for name, file_path in json_files.items():
        try:
            with open(file_path, 'r') as f:
                metrics_data = json.load(f)
            
            run_data = metrics_data["1"]
            accuracies_list = run_data["accuracies"]
            
            if not accuracies_list:
                continue
            
            rounds = [item["train_round"] for item in accuracies_list]

            if name in ["FedBCD", "Overlap-FedBCD"]:
                rounds = [item["train_round"] * 15 for item in accuracies_list]
            else:
                rounds = [item["train_round"] for item in accuracies_list]

            accuracies = [item["accuracy"] for item in accuracies_list]

            if name == "FedEncrypt":
                # There is an issue that accuracies in Fed-encrypt returns the float value not %.
                accuracies = [item["accuracy"] * 100 for item in accuracies_list]
            
            
            plt.plot(rounds, accuracies, marker='o', linestyle='-', alpha=0.8, label=name)
                
        except Exception as e:
            print(f"Error processing accuracy data for {file_path}: {e}")

    plt.xlabel('Client Training Round')
    plt.ylabel('Accuracy (%)')
    # plt.title('Model accuracy per training round')
    plt.grid(True, linestyle='--', alpha=0.6)
    
    plt.ylim(0, 100)

    plt.gca().xaxis.get_major_locator().set_params(integer=True)
    
    plt.legend(loc='lower right')

    file_name = f"{conf.PLOT_OUTPUT_FOLDER}/accuracies_plot.pdf"
    plt.savefig(file_name, bbox_inches='tight')
    plt.close()
    print(f"Plot saved in {file_name}!")

def _generate_box_plot(is_local, both_environments):
    print("> Generating box plot")

    _set_font_size(plt)
    plt.figure(figsize=(6, 5))

    data = {}

    def _read_data(is_local: bool, include_mode_in_label: bool):
        for name, file_prefix in CORRELATION_CONFIG.items():
            total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local)

            if include_mode_in_label:
                name = f"{name}_{_resolve_mode_name(is_local)}"

            # data[name] = total_metrics_data["total_carbon_emission_difference"]*1000
            # data[name] = total_metrics_data["total_energy_difference"]
            data[name] = total_metrics_data["execution_time"]

    _read_data(is_local, both_environments)

    if both_environments:
        _read_data(not is_local, both_environments)

    labels = sorted(data.keys())
    
    x = [data[label] for label in labels]

    plt.boxplot(x, tick_labels=labels)
    plt.xticks(rotation=20, ha='right')

    # plt.ylabel("Carbon Emission (gCO2e/KWh)")
    # plt.ylabel("Energy Consumption (J)")
    plt.ylabel("Execution Time (s)")
    plt.xlabel("Mitigation Strategies")

    plt.grid(True, axis='y', linestyle='--', alpha=0.7)

    file_name = f"{conf.PLOT_OUTPUT_FOLDER}/paper_ex_box_{_resolve_mode_name(is_local, both_environments)}_plot.pdf"
    plt.tight_layout()
    plt.savefig(file_name)
    plt.close()
    print(f"Plot saved in {file_name}!")

def _generate_pareto_plot(is_local, both_environments):
    print("> Generating Pareto front plot")

    def _generate(column):
        plt.figure(figsize=(6, 5))
        
        data_points = []
        
        strategy_mapping = {}
        
        num_strategies = len(CORRELATION_CONFIG) * (2 if both_environments else 1)
        colors = sns.color_palette("tab10", num_strategies)
        color_idx = 0

        def _read_data(is_local_flag, include_mode_in_label):
            nonlocal color_idx
            for name, file_prefix in CORRELATION_CONFIG.items():
                total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local_flag)
                
                if include_mode_in_label:
                    name = f"{name}_{_resolve_mode_name(is_local_flag)}"
                
                if name not in strategy_mapping:
                    strategy_mapping[name] = colors[color_idx]
                    color_idx += 1
                    
                for index, row in total_metrics_data.iterrows():
                    energy = row["total_energy_difference"]
                    compare = row[column[0]] * column[3]
                    data_points.append((name, compare, energy))
        
        # Read data based on environment flags
        _read_data(is_local, both_environments)
    
        if both_environments:
            _read_data(not is_local, both_environments)
    
        if not data_points:
            print("Warning: No data available for Pareto plot.")
            return
    
        # Sort data primarily by Carbon Emission (X) ascending, then Energy (Y) ascending
        # sorted_data = sorted(data_points, key=lambda x: (x[1], x[2]))
        # Only for accuracy
        sorted_data = sorted(data_points, key=lambda x: (-x[1], x[2]))
    
        pareto_front = []
        min_energy_so_far = float('inf')
        
        for strategy, compare, energy in sorted_data:
            if energy < min_energy_so_far:
                pareto_front.append((strategy, compare, energy))
                min_energy_so_far = energy
    
        # Only for accuracy
        pareto_front = sorted(pareto_front, key=lambda x: x[1])
    
        for strategy_name, color in strategy_mapping.items():
            s_emissions = [p[1] for p in data_points if p[0] == strategy_name]
            s_energies = [p[2] for p in data_points if p[0] == strategy_name]
            
            plt.scatter(s_emissions, s_energies, color=color, label=strategy_name, s=40, alpha=0.5, zorder=2)
    
        pareto_emissions = [row[1] for row in pareto_front]
        pareto_energies = [row[2] for row in pareto_front]
    
        plt.scatter([], [], facecolor='none', edgecolor='black', s=100, linewidth=1.5, 
                    label='Pareto Solution')
    
        for strategy, compare, energy in pareto_front:
            plt.scatter(compare, energy, color=strategy_mapping[strategy], s=100, edgecolor='black', zorder=4)
        
        plt.plot(pareto_emissions, pareto_energies, color='red', linestyle='--', linewidth=2, zorder=3)
    
        plt.xlabel(column[1], fontsize=12)
        # plt.xlabel('Accuracy (%)', fontsize=12)
        # plt.xlabel('Execution Time (s)', fontsize=12)
        # plt.xlabel('Carbon Emission (gCO2e/KWh)', fontsize=12)
        plt.ylabel('Energy Consumption (J)', fontsize=12)
        plt.grid(True, linestyle=':', alpha=0.7)
        
        plt.legend(loc='best', fontsize=10)
    
        plt.tight_layout()
        file_name = f"paper_{column[2]}_pareto_front_{_resolve_mode_name(is_local, both_environments)}"
        _save_plot(plt, file_name)
        plt.close()
                
        optimal_strategies = set([p[0] for p in pareto_front])
        print("Strategies that reached the Pareto Front:")
        for s in optimal_strategies:
            print(f"- {s}")

    for column in CORRELATION_COLUMNS:
        _generate(column)

def _generate_mean_pareto_plot(is_local, both_environments):
    print("> Generating Pareto front plot")

    def _generate(column):
        plt.figure(figsize=(6, 5))
        
        data_points = []
        
        strategy_mapping = {}
        num_strategies = len(CORRELATION_CONFIG) * (2 if both_environments else 1)
        colors = sns.color_palette("tab10", num_strategies)
        color_idx = 0
        
        def _read_data(is_local_flag, include_mode_in_label):
            nonlocal color_idx
            for name, file_prefix in CORRELATION_CONFIG.items():
                total_metrics_data, _, _ = utils.read_experiments_by_prefix(file_prefix, is_local_flag)
                
                mean_energy = total_metrics_data["total_energy_difference"].mean()
                mean_cab = total_metrics_data[column[0]].mean()*column[3]
                
                if include_mode_in_label:
                    name = f"{name}_{_resolve_mode_name(is_local_flag)}"
                    
                if name not in strategy_mapping:
                    strategy_mapping[name] = colors[color_idx]
                    color_idx += 1
                    
                data_points.append((name, mean_cab, mean_energy))

        _read_data(is_local, both_environments)

        if both_environments:
            _read_data(not is_local, both_environments)

        if not data_points:
            print("Warning: No data available for Pareto plot.")
            return

        # sorted_data = sorted(data_points, key=lambda x: (x[1], x[2]))
        # Only for accuracy
        sorted_data = sorted(data_points, key=lambda x: (-x[1], x[2]))

        pareto_front = []
        min_energy_so_far = float('inf')
        
        for strategy, mean_cab, mean_energy in sorted_data:
            if mean_energy < min_energy_so_far:
                pareto_front.append((strategy, mean_cab, mean_energy))
                min_energy_so_far = mean_energy

        # Only for accuracy
        pareto_front = sorted(pareto_front, key=lambda x: x[1])

        for strategy_name, color in strategy_mapping.items():
            s_emissions = [p[1] for p in data_points if p[0] == strategy_name]
            s_energies = [p[2] for p in data_points if p[0] == strategy_name]
            
            plt.scatter(s_emissions, s_energies, color=color, s=100, label=strategy_name, zorder=3)

        pareto_emissions = [row[1] for row in pareto_front]
        pareto_energies = [row[2] for row in pareto_front]

        plt.scatter([], [], facecolor='none', edgecolor='black', s=150, linewidth=1.5, 
                    label='Pareto Solution')

        for strategy, emissions, energy in pareto_front:
            plt.scatter(emissions, energy, color=strategy_mapping[strategy], s=150, edgecolor='black', zorder=4)

        plt.plot(pareto_emissions, pareto_energies, color='red', linestyle='--', linewidth=2, zorder=2)

        all_emissions = [row[1] for row in data_points]
        all_energies = [row[2] for row in data_points]
        labels = [row[0] for row in data_points]

        plt.xlabel(column[1], fontsize=12)
        # plt.xlabel('Mean Accuracy (%)', fontsize=12)
        # plt.xlabel('Mean Execution Time (s)', fontsize=12)
        # plt.xlabel('Mean Carbon Emission (gCO2e/KWh)', fontsize=12)
        plt.ylabel('Mean Energy Consumption (J)', fontsize=12)
        plt.grid(True, linestyle=':', alpha=0.7)
        
        plt.legend(loc='best', fontsize=10)

        plt.tight_layout()
        file_name = f"paper_{column[2]}_pareto_front_mean_{_resolve_mode_name(is_local, both_environments)}_plot"
        _save_plot(plt, file_name)
        plt.close()
        
        print("Pareto Optimal Strategies identified:")
        for p in pareto_front:
            print(f"- {p[0]}")

    for column in CORRELATION_COLUMNS:
        _generate(column)

def _get_site_mapping(config_path="../fabric/resources/node_configuration.json"):
    try:
        with open(config_path, 'r') as f:
            nodes = json.load(f)
    except FileNotFoundError:
        print(f"Warning: {config_path} not found. Using fallback mapping.")
        return {"AMST": [], "LOSA": [], "HAWI": []}

    site_mapping = {}
    for node in nodes:
        site = node.get("site")
        # Extract name if it exists (e.g., 'clientone'), otherwise use type (e.g., 'control')
        agent_name = node.get("name", node.get("type"))
        
        if site not in site_mapping:
            site_mapping[site] = []
        if agent_name not in site_mapping[site]:
            site_mapping[site].append(agent_name)
            
    return site_mapping

def get_site_metrics(active_dict, idle_dict, site_mapping, sites):
    active_means = {
        key: sum(float(v) for v in val) / len(val) if val else 0.0 
        for key, val in active_dict.items()
    }
    
    idle_means = {
        key: sum(float(v) for v in val) / len(val) if val else 0.0 
        for key, val in idle_dict.items()
    }
    
    all_keys = set(active_means.keys()).union(set(idle_means.keys()))
    net_metrics = {
        key: active_means.get(key, 0.0) - idle_means.get(key, 0.0)
        for key in all_keys
    }

    # print(net_metrics)
    
    site_sums = []
    for site in sites:
        agents = site_mapping.get(site, [])
        site_total = sum(max(0.0, active_means.get(agent, 0.0)) for agent in agents)
        site_sums.append(site_total)

    # print(site_sums)
    return site_sums

def get_dynamic_limits(totals):    
    all_tots = sorted(list(totals.values()))
    max_raw = all_tots[-1]
    
    max_gap = 0
    split_idx = 0
    for i in range(len(all_tots)-1):
        gap = all_tots[i+1] - all_tots[i]
        if gap > max_gap:
            max_gap = gap
            split_idx = i
            
    # If there is a substantial gap (e.g., more than 20% of the max value)
    if max_gap > max_raw * 0.2:
        lower_max = all_tots[split_idx] * 1.0    # 15% headroom above the lower group
        upper_min = all_tots[split_idx+1] * 1.0  # 15% floor below the upper group
    else:
        # Fallback if there is no major outlier gap
        lower_max = max_raw * 1.05
        upper_min = max_raw * 0.95
        
    return 25, 20, 25

def _generate_distributed_bar_plot(is_local, both_environments):
    print("> Generating separate stacked bar plots for Energy and Carbon")
    
    components = ["LOSA", "AMST", "HAWI"]
    site_mapping = _get_site_mapping()
    
    methods = []
    carbon_emissions = {}
    energy_consumption = {}

    def _read_data(is_local_flag, include_mode_in_label):
        for method_name, file_prefix in CORRELATION_CONFIG.items():
            _, _, data = utils.read_experiments_by_prefix(file_prefix, is_local_flag)
            
            active_energy_data = data.get("active_energy", {})
            idle_energy_data = data.get("idle_energy", {})
            active_carbon_data = {
                agent: [float(v) * 1000 for v in values] 
                for agent, values in data.get("active_carbon_emission", {}).items()
            }
            idle_carbon_data = {
                agent: [float(v) * 1000 for v in values] 
                for agent, values in data.get("idle_carbon_emission", {}).items()
            }
            
            label = f"{method_name}_{_resolve_mode_name(is_local_flag)}" if include_mode_in_label else method_name
            if label not in methods:
                methods.append(label)
                
            energy_consumption[label] = get_site_metrics(
                active_energy_data, idle_energy_data, site_mapping, components
            )
            carbon_emissions[label] = get_site_metrics(
                active_carbon_data, idle_carbon_data, site_mapping, components
            )

    _read_data(is_local, both_environments)
    if both_environments:
        _read_data(not is_local, both_environments)

    carbon_totals = {method: sum(values) for method, values in carbon_emissions.items()}
    energy_totals = {method: sum(values) for method, values in energy_consumption.items()}

    x = np.arange(len(methods))
    bar_width = 0.5  
    colors = ["#4C78A8", "#F58518", "#54A24B"]

    max_c, c_lower, c_upper = get_dynamic_limits(carbon_totals)
    max_e, e_lower, e_upper = get_dynamic_limits(energy_totals)

    # Define the configurations using the dynamic limits
    plot_configs = [
        {
            "name": "carbon",
            "data": carbon_emissions,
            "totals": carbon_totals,
            "ylabel": "Carbon emissions (g CO2e)",
            "max_raw": max_c,
            "lower_max": c_lower,
            "upper_min": c_upper,
            "hatch": "///"
        },
        {
            "name": "energy",
            "data": energy_consumption,
            "totals": energy_totals,
            "ylabel": "Energy consumption (J)",
            "max_raw": max_e,
            "lower_max": e_lower,
            "upper_min": e_upper,
            "hatch": "///"
        }
    ]

    for config in plot_configs:
        if config["name"] == "energy":
            return
        
        fig, (ax1, ax2) = plt.subplots(
            2, 1, sharex=True, figsize=(4.5, 4.0), 
            gridspec_kw={'height_ratios': [1, 3]}
        )
        fig.subplots_adjust(hspace=0.1)

        bottom_vals = np.zeros(len(methods))

        for component_index, location in enumerate(components):
            vals = [config["data"][method][component_index] for method in methods]

            for ax in (ax1, ax2):
                ax.bar(
                    x, vals, bar_width, bottom=bottom_vals,
                    label=location if ax == ax1 else "", color=colors[component_index], 
                    edgecolor="white", linewidth=0.8, hatch=config["hatch"], clip_on=True
                )

            bottom_vals += np.array(vals)

        ax1.set_ylim(config["upper_min"], config["max_raw"] * 1.05)
        ax2.set_ylim(0, config["lower_max"])
        
        for xi, method in zip(x, methods):
            tot = config["totals"][method]
            
            if tot >= config["upper_min"]:
                target_ax = ax1
                offset = (config["max_raw"] - config["upper_min"]) * 0.08
            else:
                target_ax = ax2
                offset = config["lower_max"] * 0.03
                
            prefix = "C:" if config["name"] == "carbon" else "E:"
            
            target_ax.text(
                xi, tot + offset, 
                f"{prefix} {tot:.2f}", 
                ha="center", va="bottom", fontsize=7
            )

        ax1.spines[["bottom", "top", "right"]].set_visible(False)
        ax2.spines[["top", "right"]].set_visible(False)

        ax1.xaxis.tick_top()
        ax1.tick_params(labeltop=False, bottom=False) 
        ax2.xaxis.tick_bottom()

        d = .025
        kw = dict(marker=[(-1, -d), (1, d)], markersize=10, linestyle="none", color='k', mec='k', mew=1, clip_on=False)
        
        ax1.plot([0], [0], transform=ax1.transAxes, **kw)
        ax2.plot([0], [1], transform=ax2.transAxes, **kw)

        ax2.set_xticks(x)
        ax2.set_xticklabels(methods, rotation=18, ha="right", fontsize=8)
        
        ax1.set_ylabel(config["ylabel"], fontsize=8)
        ax1.yaxis.set_label_coords(-0.15, -0.1) 

        ax1.tick_params(axis="y", labelsize=7)
        ax2.tick_params(axis="y", labelsize=7)

        ax1.grid(axis="y", linestyle=":", alpha=0.45)
        ax2.grid(axis="y", linestyle=":", alpha=0.45)

        location_legend = ax1.legend(
            title="Location", frameon=False, fontsize=6.5, 
            title_fontsize=7, ncols=1, loc="upper center"
        )
        ax1.add_artist(location_legend)

        file_name = f"{conf.PLOT_OUTPUT_FOLDER}/bar_{config['name']}_{_resolve_mode_name(is_local, both_environments)}.pdf"
        
        try:
            fig.savefig(file_name, dpi=600, bbox_inches="tight")
            print(f"Plot saved in {file_name}!")
        except ValueError as e:
            print(f"Could not save figure {config['name']} due to layout constraints: {e}")
        finally:
            plt.close(fig)

def _generate_bar_plot():
    methods = ["FedBCD", "Overlap-FedBCD", "FedEncrypt"]
    environments = ["Local", "FABRIC"]

    columns = [
        ("execution_time", "Execution Time Change (%)", "ex"),
        ("total_energy_difference", "Energy Consumption Change (%)", "en")
    ]

    def _generate(column_name, column_caption, column_prefix):
        changes = {"Local": [], "FABRIC": []}
            
        for is_local, env_name in [(True, "Local"), (False, "FABRIC")]:

            config = CORRELATION_CONFIG

            if not is_local:
                config = {
                    "Baseline": "scattered_baseline_experiment",
                    "FedBCD": "scattered_fed_bcd_experiment",
                    "Overlap-FedBCD": "scattered_overlap_fed_bcd_experiment",
                    "FedEncrypt": "scattered_es_fed_encrypt_experiment"
                }

            baseline_df, _, _ = utils.read_experiments_by_prefix(config["Baseline"], is_local)
            baseline_time = baseline_df[column_name].mean()
            
            # Calculate the percentage change for each specific method
            for method_key in ["FedBCD", "Overlap-FedBCD", "FedEncrypt"]:
                method_df, _, _ = utils.read_experiments_by_prefix(config[method_key], is_local)
                method_time = method_df[column_name].mean()
                
                percentage_change = ((method_time - baseline_time) / baseline_time) * 100
                changes[env_name].append(percentage_change)
    
        x = np.arange(len(methods))
        width = 0.34
        colors = ["#4C78A8", "#F58518"]
    
        fig, ax = plt.subplots(figsize=(2.45, 2.05), constrained_layout=True)
    
        for i, environment in enumerate(environments):
            offset = (i - 0.5) * width
    
            bars = ax.bar(
                x + offset,
                changes[environment],
                width,
                label=environment,
                color=colors[i],
                edgecolor="white",
                linewidth=0.8,
            )
    
            for bar, value in zip(bars, changes[environment]):
                ax.text(
                    bar.get_x() + bar.get_width() / 2,
                    value * 0.18,
                    f"{value:+.2f}%",
                    ha="center",
                    va="center",
                    rotation=90,
                    fontsize=5.5,
                    color="white",
                    clip_on=True,
                )
    
        ax.axhline(0, color="#333333", linewidth=1)
    
        ax.set_xticks(x)
        ax.set_xticklabels(methods, rotation=20, ha="right", fontsize=6)
        ax.set_ylabel(column_caption, fontsize=7)
        ax.tick_params(axis="y", labelsize=6)
    
        ax.legend(
            title="Execution environment",
            frameon=False,
            fontsize=5.5,
            title_fontsize=6,
            ncols=1,
            loc="upper left",
        )
    
        ax.set_yscale("symlog", linthresh=10, linscale=1)
        # ax.set_ylim(-100, 500)
    
        ax.spines[["top", "right"]].set_visible(False)
        ax.grid(axis="y", which="both", linestyle=":", alpha=0.45)
        ax.set_axisbelow(True)
    
        output_path = f"{conf.PLOT_OUTPUT_FOLDER}/{column_prefix}_bar_plot.pdf"
        fig.savefig(output_path, dpi=600, bbox_inches="tight")
        print(f"Plot saved in {output_path}!")
    
        plt.close()

    for column_name, column_caption, column_prefix in columns:
            _generate(column_name, column_caption, column_prefix)

def _set_font_size(plt):
    plt.rcParams.update({'font.size': 16})

def _save_plot(plt, file_name: str):
    output_path = f"{conf.PLOT_OUTPUT_FOLDER}/{file_name}.pdf"
    plt.savefig(output_path, bbox_inches='tight')
    print(f"Plot saved in {output_path}!")

def _resolve_args():
    parser = argparse.ArgumentParser()
    parser.add_argument("-sid", "--sidecar", action='store_true')
    parser.add_argument("-acc", "--accuracies", action='store_true')
    parser.add_argument("-cor", "--correlation", action='store_true')
    parser.add_argument("-box", "--box-plot", action='store_true')
    parser.add_argument("-par", "--pareto", action='store_true')
    parser.add_argument("-bar", "--bar-plot", action='store_true')
    utils.add_boolean_argument(parser, conf.F_ARGUMENT)
    utils.add_boolean_argument(parser, conf.BE_ARGUMENT)

    args = parser.parse_args()
    plot_sidecar = args.sidecar
    plot_accuracies = args.accuracies
    plot_correlation = args.correlation
    plot_box = args.box_plot
    plot_pareto = args.pareto
    plot_bar = args.bar_plot

    return plot_sidecar, plot_accuracies, plot_correlation, plot_box, plot_pareto, not args.fabric_mode, args.both_environments, plot_bar

if __name__ == '__main__':
    main()